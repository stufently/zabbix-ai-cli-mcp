package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/errs"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/ops"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/safety"
)

// registerWriteTool exposes the tool that applies a change in one call.
//
// It is offered only where configuration has allowed direct writes, and it
// still builds a plan first: the plan is what carries the risk class, the
// resolved resources and the preconditions that are re-checked against live
// Zabbix. Applying it immediately removes the wait for a person, not any of
// the checking.
func registerWriteTool(server *sdk.Server, opts Options) {
	names := ops.WritableNames()
	summaries := make([]string, 0, len(names))
	for _, o := range ops.Writable() {
		line := fmt.Sprintf("%s — %s", o.Name, o.Summary)
		if o.Risk == safety.RiskDestructive {
			line += " (destructive)"
		}
		summaries = append(summaries, line)
	}
	raw, err := json.Marshal(writeSchema(names))
	if err != nil {
		return
	}
	server.AddTool(&sdk.Tool{
		Name: "zabbix_write",
		Description: "Change Zabbix.\n\n" +
			"The change is described and applied in this one call, and the result says what " +
			"was done. Every change is written to an audit log the operator can read, so " +
			"prefer the narrowest operation that does the job and say plainly what you did.\n\n" +
			"Operations marked destructive remove something Zabbix cannot restore on its own. " +
			"To show the operator a change before making it, call zabbix_plan_create instead.\n\n" +
			"Available operations:\n" + strings.Join(summaries, "\n"),
		InputSchema: json.RawMessage(raw),
		Annotations: &sdk.ToolAnnotations{
			ReadOnlyHint:    false,
			DestructiveHint: boolPtr(true),
			IdempotentHint:  false,
		},
	}, func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		var in planInput
		if len(req.Params.Arguments) > 0 {
			if err := json.Unmarshal(req.Params.Arguments, &in); err != nil {
				return toolError(errs.Usage("arguments must be a JSON object: %v", err)), nil
			}
		}
		op, ok := ops.Lookup(in.Operation)
		if !ok || op.Plan == nil {
			return toolError(errs.Usage("unknown operation %q; available operations are: %s",
				in.Operation, strings.Join(names, ", "))), nil
		}
		args, err := op.Bind(in.Params)
		if err != nil {
			return toolError(err), nil
		}
		env, err := opts.EnvFor(ctx)
		if err != nil {
			return toolError(err), nil
		}
		// The registration above was decided when the server started. This is
		// the check that counts: configuration may have been tightened since,
		// and the answer here is the current one.
		if err := ops.CheckWriteAllowed(env, ops.ApplyDirect); err != nil {
			// The gate's own advice is to approve the stored plan, and no plan
			// was stored: this refusal happened before one was built.
			var e *errs.E
			if errors.As(err, &e) {
				err = e.WithSuggestion(
					"call zabbix_plan_create to describe the change, then relay its approve command")
			}
			return toolError(err), nil
		}
		plan, err := ops.CreatePlan(ctx, env, op, args)
		if err != nil {
			return toolError(err), nil
		}
		res, err := ops.Apply(ctx, env, plan, ops.ApplyOptions{
			Mode:     ops.ApplyDirect,
			Approval: safety.ApprovalMCPWrite,
		})
		if err != nil {
			return toolError(err), nil
		}
		return toolResult(res)
	})
}

func writeSchema(names []string) map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"operation": map[string]any{
				"type":        "string",
				"enum":        names,
				"description": "the change to make",
			},
			"params": map[string]any{
				"type":        "object",
				"description": "parameters for the operation; call zabbix_write with an unknown parameter to be told the accepted ones",
			},
		},
		"required":             []string{"operation"},
		"additionalProperties": false,
	}
}

func boolPtr(v bool) *bool { return &v }
