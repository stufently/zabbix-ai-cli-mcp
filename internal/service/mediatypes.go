package service

import (
	"context"
	"fmt"

	"github.com/stufently/zabbix-ai-cli-mcp/internal/errs"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/output"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/safety"
)

// PlanMediaTypeState changes only status, including for Script and Webhook.
// Repeated requests intentionally succeed so an external scheduler can restore
// a channel without having to track its current state.
func (s *Service) PlanMediaTypeState(ctx context.Context, profile, id, name string, enable bool) (*safety.Plan, error) {
	if (id == "") == (name == "") {
		return nil, errs.Usage("supply exactly one of mediatypeid or name")
	}
	query := map[string]any{"output": []string{"mediatypeid", "name", "type", "status"}}
	if id != "" {
		query["mediatypeids"] = []string{id}
	} else {
		query["filter"] = map[string]any{"name": name}
	}
	if !enable {
		query["selectUsers"] = []string{"userid", "username", "name", "surname"}
	}
	rows, err := s.configurationRows(ctx, "mediatype.get", query)
	if err != nil {
		return nil, err
	}
	if len(rows) != 1 {
		return nil, uniqueConfigurationError("media type", len(rows))
	}
	m := rows[0]
	id = fieldString(m, "mediatypeid")
	params := map[string]any{"mediatypeid": id, "status": statusValue(enable)}
	class := safety.ClassifyCall("mediatype.update", params)
	if !class.Allowed {
		return nil, DeniedMethodError("mediatype.update", class)
	}
	op := "mediatypes.disable"
	verb := "Disable"
	if enable {
		op = "mediatypes.enable"
		verb = "Enable"
	}
	p, err := safety.NewPlan(op, profile, class.Risk, class.Scope)
	if err != nil {
		return nil, err
	}
	p.Params = params
	p.ImpactCount = 1
	p.Summary = fmt.Sprintf("%s media type %q (%s, mediatypeid=%s)", verb, output.Sanitise(fieldString(m, "name")), mediaTypeLabel(fieldString(m, "type")), id)
	p.Resources = []safety.Resource{{Kind: "mediatype", ID: id, Name: output.Sanitise(fieldString(m, "name"))}}
	p.Changes = []safety.Change{{Field: "status", Before: statusLabel(fieldString(m, "status") == "0"), After: statusLabel(enable)}}
	if !enable {
		p.RequiresConfirmName = id
		p.Changes = append(p.Changes, safety.Change{Field: "warning", After: "alerts through this channel will stop while it is disabled; dependencies below are visible to the API user"})
		users, ok := m["users"].([]any)
		if !ok {
			return nil, errs.Denied("mediatype.get did not return users; cannot describe disable impact")
		}
		for _, v := range users {
			u, ok := v.(map[string]any)
			if !ok {
				return nil, errs.Internal("malformed media type user")
			}
			p.Changes = append(p.Changes, safety.Change{Field: "user", After: output.Sanitise(fmt.Sprintf("%s (%s)", fieldString(u, "username"), fieldString(u, "userid")))})
		}
		actions, err := s.configurationRows(ctx, "action.get", map[string]any{"output": []string{"actionid", "name"}, "mediatypeids": []string{id, "0"}})
		if err != nil {
			return nil, err
		}
		for _, a := range actions {
			p.Changes = append(p.Changes, safety.Change{Field: "action", After: output.Sanitise(fmt.Sprintf("%s (%s)", fieldString(a, "name"), fieldString(a, "actionid")))})
		}
		p.Changes = append(p.Changes, safety.Change{Field: "dependencies", After: fmt.Sprintf("%d users, %d potentially affected actions (this type or all media)", len(users), len(actions))})
	}
	p.Preconditions = []safety.Precondition{configurationPrecondition("media type identity and status are unchanged", "mediatype.get", map[string]any{"output": []string{"mediatypeid", "name", "type", "status"}, "mediatypeids": []string{id}}, m, []string{"mediatypeid", "name", "type", "status"})}
	return p, p.Seal()
}

func mediaTypeLabel(kind string) string {
	switch kind {
	case "0":
		return "Email"
	case "1":
		return "Script"
	case "2":
		return "SMS"
	case "4":
		return "Webhook"
	default:
		return "type " + kind
	}
}

func (s *Service) configurationRows(ctx context.Context, method string, params map[string]any) ([]map[string]any, error) {
	var rows []map[string]any
	if err := s.client.CallIdempotent(ctx, method, params, &rows); err != nil {
		return nil, errs.FromAPI(err)
	}
	return rows, nil
}

func fieldString(row map[string]any, key string) string {
	if v, ok := row[key]; ok && v != nil {
		return fmt.Sprint(v)
	}
	return ""
}

func uniqueConfigurationError(kind string, count int) error {
	if count == 0 {
		return errs.NotFound("no matching %s is visible to this API user", kind)
	}
	return errs.Usage("%d matching %s objects; use an ID to select exactly one", count, kind)
}

func configurationPrecondition(description, method string, query, row map[string]any, fields []string) safety.Precondition {
	one := 1
	expected := map[string]any{}
	for _, f := range fields {
		expected[f] = fieldString(row, f)
	}
	return safety.Precondition{Description: description, Method: method, Params: query, ExpectCount: &one, ExpectField: expected}
}
