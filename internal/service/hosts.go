package service

import (
	"context"
	"sort"
	"strings"

	"github.com/stufently/zabbix-ai-cli-mcp/internal/errs"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/output"
)

// Availability values reported for an interface or for the active agent.
const (
	AvailUnknown     = "unknown"
	AvailAvailable   = "available"
	AvailUnavailable = "unavailable"
)

var interfaceTypes = map[string]string{"1": "agent", "2": "snmp", "3": "ipmi", "4": "jmx"}

type wireHost struct {
	HostID            string          `json:"hostid"`
	Host              string          `json:"host"`
	Name              string          `json:"name"`
	Status            string          `json:"status"`
	ActiveAvailable   string          `json:"active_available"`
	MaintenanceStatus string          `json:"maintenance_status"`
	MaintenanceID     string          `json:"maintenanceid"`
	Description       string          `json:"description"`
	ProxyID           string          `json:"proxyid"`
	Interfaces        []wireInterface `json:"interfaces"`
	Tags              []wireTag       `json:"tags"`
	ParentTemplates   []wireTemplate  `json:"parentTemplates"`
}

type wireInterface struct {
	InterfaceID string `json:"interfaceid"`
	IP          string `json:"ip"`
	DNS         string `json:"dns"`
	Port        string `json:"port"`
	Type        string `json:"type"`
	Main        string `json:"main"`
	UseIP       string `json:"useip"`
	Available   string `json:"available"`
	Error       string `json:"error"`
	ErrorsFrom  string `json:"errors_from"`
}

type wireTag struct {
	Tag   string `json:"tag"`
	Value string `json:"value"`
}

type wireTemplate struct {
	TemplateID string `json:"templateid"`
	Name       string `json:"name"`
}

// Interface is a monitoring endpoint on a host.
type Interface struct {
	Type      string `json:"type"`
	Address   string `json:"address"`
	Port      string `json:"port"`
	Main      bool   `json:"main"`
	Available string `json:"available"`
	Error     string `json:"error,omitempty"`
}

// Tag is a Zabbix tag.
type Tag struct {
	Tag   string `json:"tag"`
	Value string `json:"value,omitempty"`
}

// Host is the compact projection of a Zabbix host.
//
// Fields are chosen for diagnosis, not for completeness: a raw host.get on this
// installation returns roughly twelve thousand characters per call, most of it
// configuration an agent never reads.
type Host struct {
	ID             string      `json:"hostid"`
	Host           string      `json:"host"`
	Name           string      `json:"name"`
	Monitored      bool        `json:"monitored"`
	InMaintenance  bool        `json:"in_maintenance"`
	MaintenanceID  string      `json:"maintenance_id,omitempty"`
	AgentAvailable string      `json:"agent_available"`
	Interfaces     []Interface `json:"interfaces,omitempty"`
	Tags           []Tag       `json:"tags,omitempty"`
	Templates      []string    `json:"templates,omitempty"`
	Description    string      `json:"description,omitempty"`
}

// hostOutputFields is the default projection for host.get.
var hostOutputFields = []string{
	"hostid", "host", "name", "status", "active_available",
	"maintenance_status", "maintenanceid", "description", "proxyid",
}

func (w wireHost) toHost() Host {
	h := Host{
		ID:            w.HostID,
		Host:          output.Sanitise(w.Host),
		Name:          output.Sanitise(w.Name),
		Monitored:     w.Status == "0",
		InMaintenance: w.MaintenanceStatus == "1",
		Description:   output.Sanitise(w.Description),
	}
	if h.InMaintenance {
		h.MaintenanceID = w.MaintenanceID
	}
	h.AgentAvailable = availability(w.ActiveAvailable)
	for _, wi := range w.Interfaces {
		addr := wi.IP
		if wi.UseIP == "0" && wi.DNS != "" {
			addr = wi.DNS
		}
		iface := Interface{
			Type:      interfaceTypeName(wi.Type),
			Address:   output.Sanitise(addr),
			Port:      wi.Port,
			Main:      wi.Main == "1",
			Available: availability(wi.Available),
			Error:     output.Sanitise(wi.Error),
		}
		h.Interfaces = append(h.Interfaces, iface)
		// Zabbix moved availability from the host to the interface in 5.4.
		// The agent interface is the one an operator means by "is it up".
		if iface.Type == "agent" && iface.Main && iface.Available != AvailUnknown {
			h.AgentAvailable = iface.Available
		}
	}
	for _, t := range w.Tags {
		h.Tags = append(h.Tags, Tag{Tag: output.Sanitise(t.Tag), Value: output.Sanitise(t.Value)})
	}
	for _, t := range w.ParentTemplates {
		h.Templates = append(h.Templates, output.Sanitise(t.Name))
	}
	return h
}

func availability(v string) string {
	switch v {
	case "1":
		return AvailAvailable
	case "2":
		return AvailUnavailable
	default:
		return AvailUnknown
	}
}

func interfaceTypeName(v string) string {
	if n, ok := interfaceTypes[v]; ok {
		return n
	}
	return "type" + v
}

// HostQuery selects hosts.
type HostQuery struct {
	// Search matches the technical host name or the visible name as a
	// substring, case-insensitively. Wildcards are honoured.
	Search string
	// Group filters by host group name.
	Group string
	// Limit bounds the result. Callers pass the user's limit; the query asks
	// Zabbix for one more row so that truncation is detected without a count.
	Limit int
	// WithDetail requests interfaces, tags and templates.
	WithDetail bool
	// MonitoredOnly excludes hosts whose monitoring is disabled.
	MonitoredOnly bool
}

// ListHosts returns hosts matching the query, plus whether the result was cut
// short by the limit.
func (s *Service) ListHosts(ctx context.Context, q HostQuery) ([]Host, bool, error) {
	params := map[string]any{
		"output":    hostOutputFields,
		"sortfield": "name",
		"sortorder": "ASC",
		"limit":     q.Limit + 1,
		"selectInterfaces": []string{
			"interfaceid", "ip", "dns", "port", "type", "main", "useip", "available", "error",
		},
	}
	if q.Limit <= 0 {
		delete(params, "limit")
	}
	if q.WithDetail {
		params["selectTags"] = "extend"
		params["selectParentTemplates"] = []string{"templateid", "name"}
	}
	// A single call with searchByAny covers both the technical and the visible
	// name. Substring matching is deliberate: a third of the calls against the
	// tool this replaces returned nothing because the agent had to guess an
	// exact name.
	applySearch(params, q.Search, "host", "name")
	if q.Group != "" {
		gids, err := s.hostGroupIDs(ctx, q.Group)
		if err != nil {
			return nil, false, err
		}
		if len(gids) == 0 {
			return nil, false, errs.NotFound("no host group matches %q", q.Group)
		}
		params["groupids"] = gids
	}
	if q.MonitoredOnly {
		params["filter"] = map[string]any{"status": "0"}
	}

	var wire []wireHost
	if err := s.client.CallIdempotent(ctx, "host.get", params, &wire); err != nil {
		return nil, false, errs.FromAPI(err)
	}
	hosts := make([]Host, 0, len(wire))
	for _, w := range wire {
		hosts = append(hosts, w.toHost())
	}
	rankHosts(hosts, q.Search)
	kept, truncated := output.Bound(hosts, q.Limit)
	return kept, truncated, nil
}

// rankHosts puts exact matches first, so that a search for "web01" does not
// bury it under "web01-staging" and "web01-backup".
func rankHosts(hosts []Host, search string) {
	if search == "" {
		return
	}
	needle := strings.ToLower(search)
	score := func(h Host) int {
		switch {
		case strings.EqualFold(h.Host, search), strings.EqualFold(h.Name, search):
			return 0
		case strings.HasPrefix(strings.ToLower(h.Host), needle),
			strings.HasPrefix(strings.ToLower(h.Name), needle):
			return 1
		default:
			return 2
		}
	}
	sort.SliceStable(hosts, func(i, j int) bool {
		si, sj := score(hosts[i]), score(hosts[j])
		if si != sj {
			return si < sj
		}
		return hosts[i].Name < hosts[j].Name
	})
}

// resolveHostGroups turns one pattern into the groups it names.
//
// Substring matching is what makes patterns useful, but it also means a group
// named "Linux" would silently widen into "Linux staging" and "Embedded
// Linux". An exact name therefore wins outright, and it is looked up in its
// own query on purpose: the substring search is capped, so on an installation
// with many similarly named groups the exactly named one could fall outside
// the returned window and the widening would go unnoticed.
func (s *Service) resolveHostGroups(ctx context.Context, name string) ([]namedID, error) {
	type wireGroup struct {
		GroupID string `json:"groupid"`
		Name    string `json:"name"`
	}
	var exact []wireGroup
	exactParams := map[string]any{
		"output": []string{"groupid", "name"},
		"filter": map[string]any{"name": name},
	}
	if err := s.client.CallIdempotent(ctx, "hostgroup.get", exactParams, &exact); err != nil {
		return nil, errs.FromAPI(err)
	}
	if len(exact) > 0 {
		return []namedID{{ID: exact[0].GroupID, Name: output.Sanitise(exact[0].Name)}}, nil
	}

	params := map[string]any{
		"output": []string{"groupid", "name"},
		"limit":  200,
	}
	applySearch(params, name, "name")
	var groups []wireGroup
	if err := s.client.CallIdempotent(ctx, "hostgroup.get", params, &groups); err != nil {
		return nil, errs.FromAPI(err)
	}
	// Zabbix's filter is case-sensitive, so a pattern typed in another case
	// gets its exact match here instead.
	for _, g := range groups {
		if strings.EqualFold(g.Name, name) {
			return []namedID{{ID: g.GroupID, Name: output.Sanitise(g.Name)}}, nil
		}
	}
	out := make([]namedID, 0, len(groups))
	for _, g := range groups {
		out = append(out, namedID{ID: g.GroupID, Name: output.Sanitise(g.Name)})
	}
	return out, nil
}

func (s *Service) hostGroupIDs(ctx context.Context, name string) ([]string, error) {
	groups, err := s.resolveHostGroups(ctx, name)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(groups))
	for _, g := range groups {
		ids = append(ids, g.ID)
	}
	return ids, nil
}

// ResolveHost finds exactly one host from a name, a partial name or a host ID.
//
// An ambiguous pattern is an error that lists the candidates, because silently
// picking one would let an agent act on the wrong machine.
func (s *Service) ResolveHost(ctx context.Context, pattern string) (Host, error) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return Host{}, errs.Usage("no host was given")
	}
	// An identifier is unambiguous and must win over fuzzy name matches. A
	// numeric host name containing the same digits must never redirect an
	// operation that explicitly names a host ID.
	if isNumeric(pattern) {
		var wire []wireHost
		params := map[string]any{
			"output":                hostOutputFields,
			"hostids":               []string{pattern},
			"selectInterfaces":      "extend",
			"selectTags":            "extend",
			"selectParentTemplates": []string{"templateid", "name"},
		}
		// A miss falls through to the name search on purpose, including when
		// Zabbix rejects the value outright: any digit string is a plausible
		// host ID, and a host whose visible name is a long numeric asset tag
		// would otherwise stop resolving at all.
		if err := s.client.CallIdempotent(ctx, "host.get", params, &wire); err == nil && len(wire) == 1 {
			return wire[0].toHost(), nil
		}
	}
	hosts, _, err := s.ListHosts(ctx, HostQuery{Search: pattern, Limit: 20, WithDetail: true})
	if err != nil {
		return Host{}, err
	}
	if len(hosts) == 0 {
		return Host{}, errs.HostNotFound(pattern)
	}
	if len(hosts) == 1 {
		return hosts[0], nil
	}
	// rankHosts has already sorted exact matches first.
	if strings.EqualFold(hosts[0].Host, pattern) || strings.EqualFold(hosts[0].Name, pattern) {
		if len(hosts) == 1 || !exactMatch(hosts[1], pattern) {
			return hosts[0], nil
		}
	}
	names := make([]string, 0, len(hosts))
	for _, h := range hosts {
		names = append(names, h.Name)
		if len(names) == 10 {
			names = append(names, "...")
			break
		}
	}
	return Host{}, errs.Ambiguous("%q matches %d hosts: %s", pattern, len(hosts), strings.Join(names, ", ")).
		WithSuggestion("give the exact host name, or the host ID")
}

func exactMatch(h Host, pattern string) bool {
	return strings.EqualFold(h.Host, pattern) || strings.EqualFold(h.Name, pattern)
}

func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
