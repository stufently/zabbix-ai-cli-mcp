package service

import (
	"context"
	"strconv"
	"strings"

	"github.com/stufently/zabbix-ai-cli-mcp/internal/safety"
)

// planLimitedRawCall gives raw callers the same impact description and stale
// state checks as the domain operation. Classification has checked the entire
// batch before any reads or writes, so no unsafe row is silently discarded.
func (s *Service) planLimitedRawCall(ctx context.Context, profile, method string, params any, class safety.Classification) (*safety.Plan, error) {
	p, err := safety.NewPlan("api.call", profile, class.Risk, class.Scope)
	if err != nil {
		return nil, err
	}
	p.Params = map[string]any{"method": method, "params": params}
	objects := []any{params}
	if list, ok := params.([]any); ok {
		objects = list
	}
	summaries := []string{}
	for _, v := range objects {
		obj := v.(map[string]any)
		var part *safety.Plan
		if strings.EqualFold(strings.TrimSpace(method), "mediatype.update") {
			status, _ := strconv.Atoi(strings.TrimSpace(fieldString(obj, "status")))
			part, err = s.PlanMediaTypeState(ctx, profile, fieldString(obj, "mediatypeid"), "", status == 0)
		} else {
			changes := map[string]any{}
			for k, v := range obj {
				if k != "itemid" {
					changes[k] = v
				}
			}
			part, err = s.PlanLostResources(ctx, profile, fieldString(obj, "itemid"), "", "", changes)
		}
		if err != nil {
			return nil, err
		}
		// Raw classification may be conservatively destructive (e.g. a
		// lifetime-only edit). Never weaken it after seeing the current value.
		if part.Risk == safety.RiskDestructive {
			p.Risk = safety.RiskDestructive
		}
		summaries = append(summaries, part.Summary)
		p.Changes = append(p.Changes, part.Changes...)
		p.Resources = append(p.Resources, part.Resources...)
		p.Preconditions = append(p.Preconditions, part.Preconditions...)
		p.ImpactCount += part.ImpactCount
	}
	p.Summary = strings.Join(summaries, "; ")
	if p.Risk == safety.RiskDestructive {
		p.RequiresConfirmName = method
	}
	return p, p.Seal()
}
