package service

import (
	"fmt"
	"strings"

	"context"

	"github.com/stufently/zabbix-ai-cli-mcp/internal/errs"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/safety"
)

// event.acknowledge takes a bitmask. The public surface here is an enum
// instead, because the numbers are a well-known trap: the 7.4 reference lists
// "6 - acknowledge event" while its own worked example computes 34 for
// "acknowledge and suppress", which is 32 plus 2. Acknowledge is bit 2; the 6
// in the table is acknowledge together with a message. Assembling the mask
// from named operations removes the chance of closing a problem when the
// intent was to comment on it.
const (
	ackBitClose       = 1
	ackBitAcknowledge = 2
	ackBitMessage     = 4
	ackBitSeverity    = 8
	ackBitUnack       = 16
	ackBitSuppress    = 32
	ackBitUnsuppress  = 64
)

// AckOperation is one named update to apply to an event.
type AckOperation string

// The operations an event update may carry.
const (
	AckAcknowledge   AckOperation = "ack"
	AckMessage       AckOperation = "message"
	AckClose         AckOperation = "close"
	AckSeverity      AckOperation = "severity"
	AckUnacknowledge AckOperation = "unack"
	AckSuppress      AckOperation = "suppress"
	AckUnsuppress    AckOperation = "unsuppress"
)

// AckOperations lists every accepted operation, for schemas and error text.
var AckOperations = []string{
	string(AckAcknowledge), string(AckMessage), string(AckClose),
	string(AckSeverity), string(AckUnacknowledge), string(AckSuppress), string(AckUnsuppress),
}

var ackBits = map[AckOperation]int{
	AckAcknowledge:   ackBitAcknowledge,
	AckMessage:       ackBitMessage,
	AckClose:         ackBitClose,
	AckSeverity:      ackBitSeverity,
	AckUnacknowledge: ackBitUnack,
	AckSuppress:      ackBitSuppress,
	AckUnsuppress:    ackBitUnsuppress,
}

// AckMask converts named operations into the bitmask Zabbix expects.
func AckMask(ops []AckOperation) (int, error) {
	if len(ops) == 0 {
		return 0, errs.Usage("name at least one operation: %s", strings.Join(AckOperations, ", "))
	}
	mask := 0
	seen := map[AckOperation]bool{}
	for _, op := range ops {
		bit, ok := ackBits[op]
		if !ok {
			return 0, errs.Usage("unknown operation %q; accepted operations are %s",
				op, strings.Join(AckOperations, ", "))
		}
		if seen[op] {
			continue
		}
		seen[op] = true
		mask |= bit
	}
	if seen[AckAcknowledge] && seen[AckUnacknowledge] {
		return 0, errs.Usage("acknowledge and unacknowledge cannot be combined")
	}
	if seen[AckSuppress] && seen[AckUnsuppress] {
		return 0, errs.Usage("suppress and unsuppress cannot be combined")
	}
	return mask, nil
}

// AcknowledgeRequest describes an event update.
type AcknowledgeRequest struct {
	EventID    string
	Operations []AckOperation
	Message    string
	// Severity is required when the operations include a severity change.
	Severity string
	// SuppressUntil bounds a suppression; empty means indefinite.
	SuppressUntil int64
}

// PlanAcknowledge describes an event update without performing it.
func (s *Service) PlanAcknowledge(ctx context.Context, profile string, req AcknowledgeRequest) (*safety.Plan, error) {
	if strings.TrimSpace(req.EventID) == "" {
		return nil, errs.Usage("an event identifier is required")
	}
	mask, err := AckMask(req.Operations)
	if err != nil {
		return nil, err
	}
	if mask&ackBitMessage != 0 && strings.TrimSpace(req.Message) == "" {
		return nil, errs.Usage("the message operation needs --message text")
	}
	if strings.TrimSpace(req.Message) != "" && mask&ackBitMessage == 0 {
		// Sending a message without its bit set discards it silently.
		mask |= ackBitMessage
	}
	ev, err := s.getEvent(ctx, req.EventID)
	if err != nil {
		return nil, err
	}
	sum := ev.summary()

	risk := safety.RiskWrite
	if mask&ackBitClose != 0 {
		// Closing a problem ends an incident record and cannot be undone.
		risk = safety.RiskDestructive
	}
	plan, err := safety.NewPlan("event.acknowledge", profile, risk, "acknowledge")
	if err != nil {
		return nil, err
	}
	params := map[string]any{
		"eventids": []string{req.EventID},
		"action":   mask,
	}
	if strings.TrimSpace(req.Message) != "" {
		params["message"] = req.Message
	}
	if mask&ackBitSeverity != 0 {
		if req.Severity == "" {
			return nil, errs.Usage("the severity operation needs --severity")
		}
		sev, err := SeverityValue(req.Severity)
		if err != nil {
			return nil, err
		}
		params["severity"] = sev
		plan.Changes = append(plan.Changes, safety.Change{
			Field: "severity", Before: sum.Severity, After: SeverityName(itoa(sev)),
		})
	}
	if mask&ackBitSuppress != 0 && req.SuppressUntil > 0 {
		params["suppress_until"] = req.SuppressUntil
	}
	plan.Params = params
	plan.ImpactCount = 1
	names := make([]string, 0, len(req.Operations))
	for _, op := range req.Operations {
		names = append(names, string(op))
	}
	plan.Summary = fmt.Sprintf("Update event %s (%s): %s",
		req.EventID, sum.Name, strings.Join(names, ", "))
	plan.Resources = []safety.Resource{{Kind: "event", ID: req.EventID, Name: sum.Name}}
	if len(sum.Hosts) > 0 {
		plan.Resources = append(plan.Resources,
			safety.Resource{Kind: "host", ID: sum.Hosts[0].ID, Name: sum.Hosts[0].Name})
	}
	if mask&ackBitClose != 0 {
		plan.RequiresConfirmName = req.EventID
		plan.Changes = append(plan.Changes, safety.Change{Field: "problem", Before: "open", After: "closed"})
	}
	one := 1
	plan.Preconditions = []safety.Precondition{{
		Description: "event " + req.EventID + " still exists",
		Method:      "event.get",
		Params:      map[string]any{"output": []string{"eventid"}, "eventids": []string{req.EventID}},
		ExpectCount: &one,
	}}
	return plan, plan.Seal()
}
