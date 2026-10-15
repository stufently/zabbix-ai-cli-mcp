package service

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/stufently/zabbix-ai-cli-mcp/internal/errs"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/output"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/safety"
)

// LostResourceFields are the only LLD configuration fields this operation writes.
var LostResourceFields = []string{"lifetime_type", "lifetime", "enabled_lifetime_type", "enabled_lifetime"}

// PlanLostResources describes a lifecycle-policy change without touching the
// discovery key, interval, filter or prototypes.
func (s *Service) PlanLostResources(ctx context.Context, profile, id, template, name string, changes map[string]any) (*safety.Plan, error) {
	if id != "" && (template != "" || name != "") || id == "" && (template == "" || name == "") {
		return nil, errs.Usage("supply itemid or both template and name")
	}
	// Check the caller's original fields before merging with current state.
	check := map[string]any{"itemid": "1"}
	for k, v := range changes {
		check[k] = v
	}
	if class := safety.ClassifyCall("discoveryrule.update", check); !class.Allowed {
		return nil, DeniedMethodError("discoveryrule.update", class)
	}
	fields := append([]string{"itemid", "hostid", "name", "key_", "templateid"}, LostResourceFields...)
	query := map[string]any{"output": fields}
	if id != "" {
		query["itemids"] = []string{id}
	} else {
		ts, err := s.configurationRows(ctx, "template.get", map[string]any{"output": []string{"templateid", "name"}, "filter": map[string]any{"name": template}})
		if err != nil {
			return nil, err
		}
		if len(ts) != 1 {
			return nil, uniqueConfigurationError("template", len(ts))
		}
		query["templateids"] = []string{fieldString(ts[0], "templateid")}
		query["filter"] = map[string]any{"name": name}
	}
	rows, err := s.configurationRows(ctx, "discoveryrule.get", query)
	if err != nil {
		return nil, err
	}
	if len(rows) != 1 {
		return nil, uniqueConfigurationError("discovery rule", len(rows))
	}
	rule := rows[0]
	id = fieldString(rule, "itemid")
	params := map[string]any{"itemid": id}
	for _, f := range LostResourceFields {
		current := fieldString(rule, f)
		if current == "" {
			return nil, errs.Denied("discoveryrule.get omitted %s; this server cannot describe the requested lost-resource policy", f)
		}
		params[f] = current
	}
	for f, v := range changes {
		params[f] = fmt.Sprint(v)
	}
	update := map[string]any{"itemid": id}
	for f, v := range changes {
		update[f] = fmt.Sprint(v)
	}
	class := safety.ClassifyCall("discoveryrule.update", update)
	if !class.Allowed {
		return nil, DeniedMethodError("discoveryrule.update", class)
	}
	p, err := safety.NewPlan("discoveryrules.set_lost_resources", profile, class.Risk, class.Scope)
	if err != nil {
		return nil, err
	}
	p.Params = update
	p.Preconditions = []safety.Precondition{configurationPrecondition("discovery rule identity and lifecycle policy are unchanged", "discoveryrule.get", map[string]any{"output": fields, "itemids": []string{id}}, rule, fields)}
	for _, f := range LostResourceFields {
		p.Changes = append(p.Changes, safety.Change{Field: f, Before: lifetimeLabel(f, fieldString(rule, f)), After: lifetimeLabel(f, fieldString(params, f))})
	}
	templates, err := s.configurationRows(ctx, "template.get", map[string]any{"output": []string{"templateid", "name"}, "itemids": []string{id}})
	if err != nil {
		return nil, err
	}
	hostIDs := []string{fieldString(rule, "hostid")}
	ruleIDs := map[string]bool{id: true}
	owner := "host"
	ownerName := hostIDs[0]
	if len(templates) > 0 {
		if len(templates) != 1 {
			return nil, uniqueConfigurationError("owning template", len(templates))
		}
		owner = "template"
		ownerName = output.Sanitise(fieldString(templates[0], "name"))
		templateIDs, err := s.descendantTemplates(ctx, p, fieldString(templates[0], "templateid"))
		if err != nil {
			return nil, err
		}
		hostQuery := map[string]any{"output": []string{"hostid", "name"}, "templateids": templateIDs}
		hosts, err := s.configurationRows(ctx, "host.get", hostQuery)
		if err != nil {
			return nil, err
		}
		hostSet := map[string]bool{}
		for _, h := range hosts {
			hostSet[fieldString(h, "hostid")] = true
		}
		hostIDs = sortedIDs(hostSet)
		count := len(hosts)
		p.Preconditions = append(p.Preconditions, safety.Precondition{Description: "linked hosts are unchanged", Method: "host.get", Params: hostQuery, ExpectCount: &count, ExpectIDs: map[string][]string{"hostid": hostIDs}})
		if len(hostIDs) > 0 {
			inherited, err := s.configurationRows(ctx, "discoveryrule.get", map[string]any{"output": []string{"itemid", "hostid", "key_"}, "hostids": hostIDs, "filter": map[string]any{"key_": fieldString(rule, "key_")}})
			if err != nil {
				return nil, err
			}
			// Keys are unique per host, and inherited rules retain their template's
			// key. Restricting both host set and key excludes unrelated LLD rules.
			for _, r := range inherited {
				ruleIDs[fieldString(r, "itemid")] = true
			}
		}
	}
	lost, scheduledDisable, scheduledDelete := 0, 0, 0
	if len(hostIDs) > 0 {
		items, err := s.configurationRows(ctx, "item.get", map[string]any{
			"output": []string{"itemid"}, "hostids": hostIDs, "filter": map[string]any{"flags": 4},
			"selectDiscoveryRule": []string{"itemid"}, "selectItemDiscovery": []string{"status", "ts_disable", "ts_delete"},
		})
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			parent, _ := item["discoveryRule"].(map[string]any)
			if !ruleIDs[fieldString(parent, "itemid")] {
				continue
			}
			discovery, _ := item["itemDiscovery"].(map[string]any)
			disable := positiveTimestamp(discovery, "ts_disable")
			del := positiveTimestamp(discovery, "ts_delete")
			if fieldString(discovery, "status") == "1" || disable || del {
				lost++
				if disable {
					scheduledDisable++
				}
				if del {
					scheduledDelete++
				}
			}
		}
	}
	p.ImpactCount = len(hostIDs)
	p.Summary = fmt.Sprintf("Set lost-resource policy for %s rule %q on %s; affected hosts: %d; lost items: %d (visible to the API user)", owner, output.Sanitise(fieldString(rule, "name")), ownerName, len(hostIDs), lost)
	p.Resources = []safety.Resource{{Kind: "discoveryrule", ID: id, Name: output.Sanitise(fieldString(rule, "name"))}}
	disablePolicy := fmt.Sprintf("%s (%s)", lifetimeLabel("enabled_lifetime_type", fieldString(params, "enabled_lifetime_type")), fieldString(params, "enabled_lifetime"))
	deletePolicy := fmt.Sprintf("%s (%s)", lifetimeLabel("lifetime_type", fieldString(params, "lifetime_type")), fieldString(params, "lifetime"))
	p.Changes = append(p.Changes,
		safety.Change{Field: "lost resources", After: fmt.Sprintf("lost items: %d; currently scheduled to disable: %d; scheduled to delete: %d", lost, scheduledDisable, scheduledDelete)},
		safety.Change{Field: "effect", After: disablePolicy + "; " + deletePolicy +
			"; policy is processed by Zabbix on discovery, not an execute-now command; already disabled items are not re-enabled by choosing never"},
	)
	if class.Risk == safety.RiskDestructive {
		p.RequiresConfirmName = id
		p.Changes = append(p.Changes, safety.Change{Field: "warning", After: "deleting lost resources permanently removes the items and their history"})
	}
	return p, p.Seal()
}

func (s *Service) descendantTemplates(ctx context.Context, p *safety.Plan, root string) ([]string, error) {
	seen := map[string]bool{root: true}
	pending := []string{root}
	for len(pending) > 0 {
		query := map[string]any{"output": []string{"templateid"}, "parentTemplateids": pending}
		rows, err := s.configurationRows(ctx, "template.get", query)
		if err != nil {
			return nil, err
		}
		count := len(rows)
		p.Preconditions = append(p.Preconditions, safety.Precondition{Description: "template inheritance links are unchanged", Method: "template.get", Params: query, ExpectCount: &count, ExpectIDs: map[string][]string{"templateid": configurationIDs(rows, "templateid")}})
		next := []string{}
		for _, r := range rows {
			id := fieldString(r, "templateid")
			if !seen[id] {
				seen[id] = true
				next = append(next, id)
			}
		}
		pending = next
	}
	return sortedIDs(seen), nil
}

func sortedIDs(ids map[string]bool) []string {
	out := make([]string, 0, len(ids))
	for id := range ids {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func positiveTimestamp(row map[string]any, key string) bool {
	v := fieldString(row, key)
	return v != "" && v != "0"
}

func lifetimeLabel(field, value string) string {
	if field != "lifetime_type" && field != "enabled_lifetime_type" {
		return value
	}
	verb := "delete"
	if field == "enabled_lifetime_type" {
		verb = "disable"
	}
	mode, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return value
	}
	switch mode {
	case 0:
		return verb + " after lifetime"
	case 1:
		return "never " + verb
	case 2:
		return verb + " immediately"
	default:
		return value
	}
}

func configurationIDs(rows []map[string]any, field string) []string {
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, fieldString(row, field))
	}
	sort.Strings(ids)
	return ids
}
