// Package mcp exposes the operation registry over the Model Context Protocol.
//
// Every tool here is a thin adapter over the same operations the CLI runs.
// There is no second implementation of anything, and no tool can perform a
// write: a write request produces a plan that a person approves at a terminal.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/errs"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/ops"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/opspec"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/output"
)

// Options configure the server.
type Options struct {
	Version string
	// ReadOnly withholds the planning tool entirely, so a client cannot even
	// describe a change.
	ReadOnly bool
	// EnvFor builds a fresh environment per call, so a long-lived server picks
	// up credential changes without a restart.
	EnvFor func(ctx context.Context) (*opspec.Env, error)
}

// NewServer builds an MCP server exposing the registry.
func NewServer(opts Options) *sdk.Server {
	server := sdk.NewServer(&sdk.Implementation{
		Name:    "zabbix-ai-cli-mcp",
		Version: opts.Version,
		Title:   "Zabbix",
	}, &sdk.ServerOptions{
		Instructions: "Task-shaped access to Zabbix. Prefer the high-level tools over zabbix_api_call: " +
			"they bound their output and resolve names for you. Nothing here can change Zabbix. " +
			"To request a change, call zabbix_plan_create and ask the operator to run the approve " +
			"command it returns; you cannot approve it yourself.",
	})

	for _, op := range ops.All() {
		if op.MCPTool == "" {
			continue
		}
		registerRead(server, opts, op)
	}
	if !opts.ReadOnly {
		registerPlanTools(server, opts)
	}
	return server
}

func registerRead(server *sdk.Server, opts Options, op *opspec.Operation) {
	schema, err := json.Marshal(op.InputSchema())
	if err != nil {
		return
	}
	description := op.Summary
	if op.Long != "" {
		description += "\n\n" + op.Long
	}
	readOnly := true
	server.AddTool(&sdk.Tool{
		Name:        op.MCPTool,
		Description: description,
		InputSchema: json.RawMessage(schema),
		Annotations: &sdk.ToolAnnotations{ReadOnlyHint: readOnly},
	}, func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		input, err := decodeArguments(req)
		if err != nil {
			return toolError(err), nil
		}
		args, err := op.Bind(input)
		if err != nil {
			return toolError(err), nil
		}
		// A method this program refuses outright is not a write awaiting
		// approval. Saying so here stops the caller being sent to the planning
		// tool only to be refused there for the real reason.
		if err := op.Refuse(args); err != nil {
			return toolError(err), nil
		}
		// The escape hatch is read-only over MCP. A write travels through the
		// planning tool like every other change.
		if op.Writes(args) {
			return toolError(errs.Denied(
				"%s is a write and cannot run through this tool", op.Name).
				WithSuggestion("call zabbix_plan_create instead")), nil
		}
		env, err := opts.EnvFor(ctx)
		if err != nil {
			return toolError(err), nil
		}
		res, err := ops.RunRead(ctx, env, op, args)
		if err != nil {
			return toolError(err), nil
		}
		return toolResult(res)
	})
}

// planInput is the argument shape of the planning tool.
type planInput struct {
	Operation string         `json:"operation"`
	Params    map[string]any `json:"params"`
}

func registerPlanTools(server *sdk.Server, opts Options) {
	names := ops.WritableNames()
	summaries := make([]string, 0, len(names))
	for _, o := range ops.Writable() {
		summaries = append(summaries, fmt.Sprintf("%s — %s", o.Name, o.Summary))
	}
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"operation": map[string]any{
				"type":        "string",
				"enum":        names,
				"description": "the change to describe",
			},
			"params": map[string]any{
				"type":        "object",
				"description": "parameters for the operation; call zabbix_plan_create with an unknown parameter to be told the accepted ones",
			},
		},
		"required":             []string{"operation"},
		"additionalProperties": false,
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		return
	}
	readOnly := true
	server.AddTool(&sdk.Tool{
		Name: "zabbix_plan_create",
		Description: "Describe a change to Zabbix without making it.\n\n" +
			"Returns a plan identifier and the exact command the operator must run to apply it. " +
			"You cannot apply it yourself and there is no parameter that would let you: approval " +
			"happens at a terminal, outside this conversation. Relay the approve command verbatim.\n\n" +
			"Available operations:\n" + strings.Join(summaries, "\n"),
		InputSchema: json.RawMessage(raw),
		// The tool itself changes nothing; it only writes a plan file.
		Annotations: &sdk.ToolAnnotations{ReadOnlyHint: readOnly},
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
		plan, err := ops.CreatePlan(ctx, env, op, args)
		if err != nil {
			return toolError(err), nil
		}
		return toolResult(ops.PlanOutput(env, plan))
	})

	statusSchema, err := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"plan_id": map[string]any{"type": "string", "description": "identifier returned by zabbix_plan_create"},
		},
		"required":             []string{"plan_id"},
		"additionalProperties": false,
	})
	if err != nil {
		return
	}
	server.AddTool(&sdk.Tool{
		Name: "zabbix_plan_status",
		Description: "Check whether a plan is still waiting, was applied, or expired. " +
			"Poll this after asking the operator to approve a change.",
		InputSchema: json.RawMessage(statusSchema),
		Annotations: &sdk.ToolAnnotations{ReadOnlyHint: readOnly},
	}, func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		var in struct {
			PlanID string `json:"plan_id"`
		}
		if len(req.Params.Arguments) > 0 {
			if err := json.Unmarshal(req.Params.Arguments, &in); err != nil {
				return toolError(errs.Usage("arguments must be a JSON object: %v", err)), nil
			}
		}
		env, err := opts.EnvFor(ctx)
		if err != nil {
			return toolError(err), nil
		}
		res, err := planStatus(env, in.PlanID)
		if err != nil {
			return toolError(err), nil
		}
		return toolResult(res)
	})
}

func planStatus(env *opspec.Env, planID string) (*output.Result, error) {
	if strings.TrimSpace(planID) == "" {
		return nil, errs.Usage("plan_id is required")
	}
	data := map[string]any{"plan_id": planID}
	if env.Plans != nil {
		plan, err := env.Plans.Load(planID)
		if err == nil {
			if plan.Expired(time.Now()) {
				data["status"] = "expired"
				data["expired_at"] = plan.ExpiresAt
			} else {
				data["status"] = "awaiting approval"
				data["approve_command"] = ops.ApproveCommand(plan)
			}
			data["summary"] = plan.Summary
			data["expires_at"] = plan.ExpiresAt
			res := &output.Result{Data: data}
			res.Meta.Returned = 1
			return res, nil
		}
		var e *errs.E
		if !errors.As(err, &e) || e.Code != errs.CodePlanNotFound {
			return nil, err
		}
		claimed, claimErr := env.Plans.Claimed(planID)
		if claimErr != nil {
			return nil, claimErr
		}
		// A claim only means "applying" while no outcome has been recorded.
		// Discarding the claim can fail after a change was applied, and a
		// leftover claim survives two whole TTLs, so reporting "check again"
		// on the strength of the file alone gives advice that never comes
		// true. The audit log is the authority once it has an entry.
		if claimed && !auditKnows(env, planID) {
			data["status"] = "applying"
			data["detail"] = "the plan has been claimed by an applier; check again for its audited outcome"
			res := &output.Result{Data: data}
			res.Meta.Returned = 1
			return res, nil
		}
	}
	if env.Audit != nil {
		entry, err := env.Audit.Find(planID)
		if err != nil {
			return nil, err
		}
		if entry != nil {
			data["status"] = entry.Outcome
			data["applied_at"] = entry.Time
			data["operation"] = entry.Operation
			if entry.Error != "" {
				data["error"] = entry.Error
			}
			res := &output.Result{Data: data}
			res.Meta.Returned = 1
			return res, nil
		}
	}
	data["status"] = "gone"
	data["detail"] = "the plan is neither outstanding nor recorded as applied; it most likely expired or was rejected"
	res := &output.Result{Data: data}
	res.Meta.Returned = 1
	return res, nil
}

func decodeArguments(req *sdk.CallToolRequest) (map[string]any, error) {
	input := map[string]any{}
	if len(req.Params.Arguments) == 0 {
		return input, nil
	}
	if err := json.Unmarshal(req.Params.Arguments, &input); err != nil {
		return nil, errs.Usage("arguments must be a JSON object: %v", err)
	}
	return input, nil
}

// toolResult renders an operation result as both structured content and text.
// Clients that understand structured output get the envelope; the rest get the
// same JSON as text, so no client sees a different answer.
func toolResult(res *output.Result) (*sdk.CallToolResult, error) {
	env := output.Envelope{OK: true, Data: res.Data, Warnings: res.Warnings, Meta: res.Meta}
	if env.Warnings == nil {
		env.Warnings = []string{}
	}
	encoded, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return toolError(errs.Internal("could not encode the result: %v", err)), nil
	}
	return &sdk.CallToolResult{
		Content:           []sdk.Content{&sdk.TextContent{Text: string(encoded)}},
		StructuredContent: env,
	}, nil
}

// toolError reports a failure inside the result rather than as a protocol
// error, so the caller receives the same structured error contract the CLI
// prints.
func toolError(err error) *sdk.CallToolResult {
	e := errs.FromAPI(err)
	body := output.ErrorEnvelope{
		OK: false,
		Error: output.ErrorEnvelopeBody{
			Code: e.Code, Message: e.Message, Retryable: e.Retryable, Suggestion: e.Suggestion,
		},
	}
	encoded, mErr := json.MarshalIndent(body, "", "  ")
	if mErr != nil {
		encoded = []byte(`{"ok":false,"error":{"code":"INTERNAL_ERROR","message":"unrenderable error"}}`)
	}
	return &sdk.CallToolResult{
		IsError:           true,
		Content:           []sdk.Content{&sdk.TextContent{Text: string(encoded)}},
		StructuredContent: body,
	}
}

// auditKnows reports whether the audit log already records an outcome for a
// plan. A failure to read the log is treated as "no entry": the caller falls
// through to its own audit lookup, which reports the failure properly.
func auditKnows(env *opspec.Env, planID string) bool {
	if env.Audit == nil {
		return false
	}
	entry, err := env.Audit.Find(planID)
	return err == nil && entry != nil
}
