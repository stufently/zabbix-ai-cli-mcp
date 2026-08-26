package service

import (
	"context"

	"github.com/stufently/zabbix-ai-cli-mcp/internal/errs"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/output"
)

// UnreachableHost is a monitored host Zabbix cannot currently poll.
type UnreachableHost struct {
	Host       string      `json:"host"`
	HostID     string      `json:"hostid"`
	Interfaces []Interface `json:"interfaces"`
	// InMaintenance matters because an unreachable host inside a maintenance
	// window is usually expected rather than broken.
	InMaintenance bool `json:"in_maintenance"`
}

// ListUnreachable returns monitored hosts with at least one unavailable
// interface.
//
// Availability lives on the interface, not on the host: host.available was
// removed in 5.4, and code that still reads it silently sees nothing.
func (s *Service) ListUnreachable(ctx context.Context, limit int) ([]UnreachableHost, bool, error) {
	if limit <= 0 {
		limit = 50
	}
	ifaceParams := map[string]any{
		"output": []string{"interfaceid", "hostid", "ip", "dns", "port", "type", "main", "useip", "available", "error"},
		"filter": map[string]any{"available": "2"},
		"limit":  1000,
	}
	type wireInterfaceWithHost struct {
		wireInterface
		HostID string `json:"hostid"`
	}
	var withHost []wireInterfaceWithHost
	if err := s.client.CallIdempotent(ctx, "hostinterface.get", ifaceParams, &withHost); err != nil {
		return nil, false, errs.FromAPI(err)
	}

	byHost := map[string][]wireInterface{}
	var hostIDs []string
	for _, w := range withHost {
		if _, seen := byHost[w.HostID]; !seen {
			hostIDs = append(hostIDs, w.HostID)
		}
		byHost[w.HostID] = append(byHost[w.HostID], w.wireInterface)
	}
	if len(hostIDs) == 0 {
		return []UnreachableHost{}, false, nil
	}

	var hosts []wireHost
	hostParams := map[string]any{
		"output":    hostOutputFields,
		"hostids":   hostIDs,
		"filter":    map[string]any{"status": "0"},
		"sortfield": "name",
	}
	if err := s.client.CallIdempotent(ctx, "host.get", hostParams, &hosts); err != nil {
		return nil, false, errs.FromAPI(err)
	}

	out := make([]UnreachableHost, 0, len(hosts))
	for _, w := range hosts {
		h := w.toHost()
		entry := UnreachableHost{Host: h.Name, HostID: h.ID, InMaintenance: h.InMaintenance}
		for _, wi := range byHost[w.HostID] {
			addr := wi.IP
			if wi.UseIP == "0" && wi.DNS != "" {
				addr = wi.DNS
			}
			entry.Interfaces = append(entry.Interfaces, Interface{
				Type:      interfaceTypeName(wi.Type),
				Address:   output.Sanitise(addr),
				Port:      wi.Port,
				Main:      wi.Main == "1",
				Available: availability(wi.Available),
				Error:     output.Sanitise(wi.Error),
			})
		}
		out = append(out, entry)
	}
	kept, truncated := output.Bound(out, limit)
	return kept, truncated, nil
}
