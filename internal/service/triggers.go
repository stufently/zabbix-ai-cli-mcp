package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/stufently/zabbix-ai-cli-mcp/internal/errs"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/output"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/safety"
)

// Trigger is the compact projection of a Zabbix trigger.
type Trigger struct {
	ID          string        `json:"triggerid"`
	Description string        `json:"description"`
	Severity    string        `json:"severity"`
	Enabled     bool          `json:"enabled"`
	State       string        `json:"state"`
	Value       string        `json:"value"`
	Error       string        `json:"error,omitempty"`
	Hosts       []ProblemHost `json:"hosts,omitempty"`
}

type wireTrigger struct {
	TriggerID   string `json:"triggerid"`
	Description string `json:"description"`
	Priority    string `json:"priority"`
	Status      string `json:"status"`
	State       string `json:"state"`
	Value       string `json:"value"`
	Error       string `json:"error"`
	Hosts       []struct {
		HostID string `json:"hostid"`
		Name   string `json:"name"`
	} `json:"hosts"`
}

// TriggerQuery selects triggers.
type TriggerQuery struct {
	Host   string
	Search string
	Limit  int
	// ProblemsOnly restricts the result to triggers currently in a problem
	// state.
	ProblemsOnly bool
}

// ListTriggers returns triggers matching the query.
func (s *Service) ListTriggers(ctx context.Context, q TriggerQuery) ([]Trigger, bool, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 50
	}
	params := map[string]any{
		"output":            []string{"triggerid", "description", "priority", "status", "state", "value", "error"},
		"selectHosts":       []string{"hostid", "name"},
		"sortfield":         "description",
		"limit":             limit + 1,
		"expandDescription": true,
	}
	if q.Host != "" {
		h, err := s.ResolveHost(ctx, q.Host)
		if err != nil {
			return nil, false, err
		}
		params["hostids"] = []string{h.ID}
	}
	applySearch(params, q.Search, "description")
	if q.ProblemsOnly {
		params["filter"] = map[string]any{"value": 1}
	}
	var wire []wireTrigger
	if err := s.client.CallIdempotent(ctx, "trigger.get", params, &wire); err != nil {
		return nil, false, errs.FromAPI(err)
	}
	out := make([]Trigger, 0, len(wire))
	for _, w := range wire {
		out = append(out, w.toTrigger())
	}
	kept, truncated := output.Bound(out, limit)
	return kept, truncated, nil
}

func (w wireTrigger) toTrigger() Trigger {
	t := Trigger{
		ID:          w.TriggerID,
		Description: output.Sanitise(w.Description),
		Severity:    SeverityName(w.Priority),
		Enabled:     w.Status == "0",
		Error:       output.Sanitise(w.Error),
	}
	if w.State == "1" {
		t.State = "unknown"
	} else {
		t.State = "normal"
	}
	if w.Value == "1" {
		t.Value = "problem"
	} else {
		t.Value = "ok"
	}
	for _, h := range w.Hosts {
		t.Hosts = append(t.Hosts, ProblemHost{ID: h.HostID, Name: output.Sanitise(h.Name)})
	}
	return t
}

// GetTrigger returns one trigger by identifier.
func (s *Service) GetTrigger(ctx context.Context, id string) (*Trigger, error) {
	params := map[string]any{
		"output":            []string{"triggerid", "description", "priority", "status", "state", "value", "error"},
		"triggerids":        []string{id},
		"selectHosts":       []string{"hostid", "name"},
		"expandDescription": true,
	}
	var wire []wireTrigger
	if err := s.client.CallIdempotent(ctx, "trigger.get", params, &wire); err != nil {
		return nil, errs.FromAPI(err)
	}
	if len(wire) == 0 {
		return nil, errs.NotFound("no trigger has ID %s", id)
	}
	t := wire[0].toTrigger()
	return &t, nil
}

// PlanTriggerState describes enabling or disabling a trigger.
//
// Disabling is treated as destructive rather than as an ordinary write: a
// silenced trigger produces no alert and no record that it would have, which
// is exactly how an outage goes unnoticed.
func (s *Service) PlanTriggerState(ctx context.Context, profile, id string, enable bool) (*safety.Plan, error) {
	if strings.TrimSpace(id) == "" {
		return nil, errs.Usage("a trigger identifier is required")
	}
	t, err := s.GetTrigger(ctx, id)
	if err != nil {
		return nil, err
	}
	if t.Enabled == enable {
		state := "disabled"
		if enable {
			state = "enabled"
		}
		return nil, errs.Usage("trigger %s is already %s", id, state)
	}

	operation := "trigger.disable"
	risk := safety.RiskDestructive
	status := "1"
	if enable {
		operation = "trigger.enable"
		risk = safety.RiskWrite
		status = "0"
	}
	plan, err := safety.NewPlan(operation, profile, risk, "configuration")
	if err != nil {
		return nil, err
	}
	plan.Params = map[string]any{"triggerid": id, "status": status}
	plan.ImpactCount = 1
	verb := "Disable"
	if enable {
		verb = "Enable"
	}
	host := ""
	if len(t.Hosts) > 0 {
		host = " on " + t.Hosts[0].Name
	}
	plan.Summary = fmt.Sprintf("%s trigger %q%s", verb, t.Description, host)
	plan.Resources = []safety.Resource{{Kind: "trigger", ID: id, Name: t.Description}}
	plan.Changes = []safety.Change{{Field: "status", Before: statusLabel(t.Enabled), After: statusLabel(enable)}}
	if !enable {
		plan.RequiresConfirmName = id
		plan.Changes = append(plan.Changes, safety.Change{
			Field: "note",
			After: "no alert will be raised by this trigger while it is disabled",
		})
	}
	one := 1
	plan.Preconditions = []safety.Precondition{{
		Description: fmt.Sprintf("trigger %s is still %q", id, t.Description),
		Method:      "trigger.get",
		Params: map[string]any{
			"output": []string{"triggerid", "status"}, "triggerids": []string{id},
		},
		ExpectCount: &one,
		ExpectField: map[string]any{"status": statusValue(t.Enabled)},
	}}
	return plan, plan.Seal()
}

func statusLabel(enabled bool) string {
	if enabled {
		return "enabled"
	}
	return "disabled"
}

func statusValue(enabled bool) string {
	if enabled {
		return "0"
	}
	return "1"
}
