package service

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/stufently/zabbix-ai-cli-mcp/internal/errs"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/safety"
)

// planExecution maps a planned operation onto the Zabbix method that performs
// it. Keeping the mapping in one table means a plan file cannot name a method
// that was never intended to be reachable.
var planExecution = map[string]string{
	"maintenance.create": "maintenance.create",
	"maintenance.extend": "maintenance.update",
	"maintenance.expire": "maintenance.update",
	"maintenance.delete": "maintenance.delete",
	"event.acknowledge":  "event.acknowledge",
	"trigger.enable":     "trigger.update",
	"trigger.disable":    "trigger.update",
	"api.call":           "", // the method travels inside the plan
}

// CheckPreconditions re-reads the world and reports whether the plan still
// describes it.
func (s *Service) CheckPreconditions(ctx context.Context, plan *safety.Plan) error {
	for _, pc := range plan.Preconditions {
		var rows []map[string]any
		if err := s.client.CallIdempotent(ctx, pc.Method, pc.Params, &rows); err != nil {
			return errs.New(errs.CodePlanStale, errs.ExitFailure,
				"could not verify that %s: %v", pc.Description, errs.FromAPI(err)).
				WithSuggestion("re-run the command to build a fresh plan")
		}
		if pc.ExpectCount != nil && len(rows) != *pc.ExpectCount {
			return errs.New(errs.CodePlanStale, errs.ExitFailure,
				"the world changed since the plan was made: expected %d result(s) confirming that %s, found %d",
				*pc.ExpectCount, pc.Description, len(rows)).
				WithSuggestion("re-run the command to build a fresh plan")
		}
		if len(pc.ExpectField) > 0 {
			if len(rows) == 0 {
				return errs.New(errs.CodePlanStale, errs.ExitFailure,
					"the object this plan targets no longer exists (%s)", pc.Description)
			}
			for field, want := range pc.ExpectField {
				got := fmt.Sprintf("%v", rows[0][field])
				if got != fmt.Sprintf("%v", want) {
					return errs.New(errs.CodePlanStale, errs.ExitFailure,
						"the object this plan targets changed: %s is now %q, the plan expected %q",
						field, got, want).
						WithSuggestion("re-run the command to build a fresh plan")
				}
			}
		}
	}
	return nil
}

// ApplyPlan executes a verified plan.
//
// The plan's hash and deadline are checked by the caller before this point;
// preconditions are checked here, immediately before the write, so the gap
// between verification and execution is as small as it can be.
func (s *Service) ApplyPlan(ctx context.Context, plan *safety.Plan) (any, error) {
	method, ok := planExecution[plan.Operation]
	if !ok {
		return nil, errs.Internal("plan names an operation this build cannot execute: %s", plan.Operation)
	}
	if err := s.CheckPreconditions(ctx, plan); err != nil {
		return nil, err
	}

	var params any = plan.Params
	switch plan.Operation {
	case "maintenance.delete":
		// maintenance.delete takes a flat array of identifiers. Passing an
		// object here is the single most common reason a delete silently
		// fails to be callable at all.
		ids, err := stringSlice(plan.Params["maintenanceids"])
		if err != nil {
			return nil, errs.Internal("plan %s has malformed identifiers", plan.ID)
		}
		params = ids
	case "api.call":
		m, _ := plan.Params["method"].(string)
		if m == "" {
			return nil, errs.Internal("plan %s names no API method", plan.ID)
		}
		method = m
		params = plan.Params["params"]
	}

	var result json.RawMessage
	// A write is never retried: after a transport failure there is no way to
	// know whether Zabbix already applied it.
	if err := s.client.Call(ctx, method, params, &result); err != nil {
		return nil, errs.FromAPI(err)
	}
	var decoded any
	if err := json.Unmarshal(result, &decoded); err != nil {
		return string(result), nil
	}
	return decoded, nil
}

func stringSlice(v any) ([]string, error) {
	switch t := v.(type) {
	case []string:
		return t, nil
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			s, ok := e.(string)
			if !ok {
				return nil, fmt.Errorf("not a string: %v", e)
			}
			out = append(out, s)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("not a list: %T", v)
	}
}
