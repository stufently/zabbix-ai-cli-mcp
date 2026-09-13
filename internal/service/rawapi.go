package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/stufently/zabbix-ai-cli-mcp/internal/errs"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/safety"
)

// RawResult carries the escape hatch's answer plus what the registry decided
// about the method, so a caller can see why a call was permitted.
type RawResult struct {
	Method string `json:"method"`
	Risk   string `json:"risk"`
	Result any    `json:"result"`
}

// RawRead performs a read-only raw API call.
//
// The escape hatch exists because the alternative is worse: the first task the
// high-level commands do not cover is otherwise done with curl and a token
// copied out of a container, which leaves no audit trail at all.
func (s *Service) RawRead(ctx context.Context, method string, params any) (*RawResult, error) {
	class := safety.ClassifyCall(method, params)
	if !class.Allowed {
		return nil, DeniedMethodError(method, class)
	}
	if class.Risk != safety.RiskRead {
		return nil, errs.Denied("%s is a %s method and cannot run as a read", method, class.Risk).
			WithSuggestion("plan it instead: 'zabbix-ai-cli-mcp api call %s --params ... --apply'", method)
	}
	var raw json.RawMessage
	if err := s.client.CallIdempotent(ctx, method, params, &raw); err != nil {
		return nil, errs.FromAPI(err)
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		decoded = string(raw)
	}
	return &RawResult{Method: method, Risk: string(class.Risk), Result: decoded}, nil
}

// PlanRawCall describes a raw write without performing it.
func (s *Service) PlanRawCall(ctx context.Context, profile, method string, params any) (*safety.Plan, error) {
	class := safety.ClassifyCall(method, params)
	if !class.Allowed {
		return nil, DeniedMethodError(method, class)
	}
	if class.Risk == safety.RiskRead {
		return nil, errs.Usage("%s is a read method; it runs immediately and needs no plan", method)
	}
	plan, err := safety.NewPlan("api.call", profile, class.Risk, class.Scope)
	if err != nil {
		return nil, err
	}
	plan.Params = map[string]any{"method": method, "params": params}
	plan.ImpactCount = countAffected(params)
	plan.Summary = fmt.Sprintf("Call the Zabbix API method %s directly", method)
	encoded, _ := json.Marshal(params)
	plan.Changes = []safety.Change{{Field: "parameters", After: truncateForDisplay(string(encoded))}}
	if class.Risk == safety.RiskDestructive {
		plan.RequiresConfirmName = method
	}
	return plan, plan.Seal()
}

// DeniedMethodError explains a method this program will not call at all, with
// the reason from the registry rather than a generic refusal.
func DeniedMethodError(method string, class safety.Classification) error {
	e := errs.Denied("the method %q is refused: %s", method, class.Reason)
	if strings.Contains(class.Reason, "risk registry") {
		return e.WithSuggestion("run 'zabbix-ai-cli-mcp schema api-methods' to list the methods this tool will call")
	}
	return e
}

// countAffected estimates how many objects a raw write touches, so that a plan
// can say "this changes 40 things" rather than leaving the reader to count.
func countAffected(params any) int {
	switch t := params.(type) {
	case []any:
		return len(t)
	case map[string]any:
		for _, key := range []string{"hostids", "itemids", "triggerids", "maintenanceids", "eventids", "groupids"} {
			if v, ok := t[key]; ok {
				if list, ok := v.([]any); ok {
					return len(list)
				}
				return 1
			}
		}
		return 1
	default:
		return 1
	}
}

func truncateForDisplay(s string) string {
	const max = 300
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// ParseParams reads the --params argument, which is JSON.
//
// It is parsed here rather than interpolated into a command line anywhere:
// nothing in this program builds a shell string out of caller input.
func ParseParams(raw string) (any, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return map[string]any{}, nil
	}
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return nil, errs.Usage("--params must be JSON: %v", err)
	}
	return v, nil
}
