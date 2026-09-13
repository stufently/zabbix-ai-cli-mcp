package ops

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/stufently/zabbix-ai-cli-mcp/internal/api"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/errs"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/opspec"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/output"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/safety"
)

// Status values reported in the data of a write operation.
const (
	StatusPlanned = "planned"
	StatusApplied = "applied"
)

// PlanResult is what a write operation returns before anything has changed.
type PlanResult struct {
	Status  string          `json:"status"`
	PlanID  string          `json:"plan_id"`
	Summary string          `json:"summary"`
	Risk    string          `json:"risk"`
	Changes []safety.Change `json:"changes,omitempty"`
	Impact  int             `json:"impact_count"`
	Expires string          `json:"expires_at"`
	// Approve is the exact command a person must run. It is spelled out so
	// that an agent can relay it rather than invent one.
	Approve string `json:"approve_command"`
}

// AppliedResult is what a write operation returns once it has run.
type AppliedResult struct {
	Status  string `json:"status"`
	PlanID  string `json:"plan_id"`
	Summary string `json:"summary"`
	Result  any    `json:"result"`
}

// RunRead executes a read operation.
func RunRead(ctx context.Context, env *opspec.Env, op *opspec.Operation, args *opspec.Args) (*output.Result, error) {
	if op.Run == nil {
		return nil, errs.Internal("%s has no read implementation", op.Name)
	}
	start := time.Now()
	res, err := op.Run(ctx, env, args)
	if err != nil {
		return nil, err
	}
	res.Meta.Profile = env.Profile
	res.Meta.ElapsedMS = time.Since(start).Milliseconds()
	if v, verr := env.Service.Version(ctx); verr == nil {
		res.Meta.ZabbixVersion = v.String()
	}
	return res, nil
}

// CreatePlan validates the request and stores a plan for later approval.
func CreatePlan(ctx context.Context, env *opspec.Env, op *opspec.Operation, args *opspec.Args) (*safety.Plan, error) {
	if op.Plan == nil {
		return nil, errs.Internal("%s is not a write operation", op.Name)
	}
	if err := CheckScope(env, op); err != nil {
		return nil, err
	}
	plan, err := op.Plan(ctx, env, args)
	if err != nil {
		return nil, err
	}
	// The plan knows the scope it really needs, which for the escape hatch
	// depends on the method it was handed rather than on the operation.
	if err := CheckPlanScope(env, plan); err != nil {
		return nil, err
	}
	if env.Plans != nil {
		if err := env.Plans.Save(plan); err != nil {
			return nil, err
		}
	}
	return plan, nil
}

// PlanOutput renders a stored plan as an operation result.
func PlanOutput(env *opspec.Env, plan *safety.Plan) *output.Result {
	res := &output.Result{
		Data: PlanResult{
			Status:  StatusPlanned,
			PlanID:  plan.ID,
			Summary: plan.Summary,
			Risk:    string(plan.Risk),
			Changes: plan.Changes,
			Impact:  plan.ImpactCount,
			Expires: plan.ExpiresAt.Format(time.RFC3339),
			Approve: ApproveCommand(plan),
		},
	}
	res.Meta.Returned = 1
	res.Meta.Profile = env.Profile
	res.Table = &output.Table{
		Headers: []string{"PLAN"},
		Rows:    [][]string{{plan.Describe()}},
		Notes: []string{
			"Nothing has changed yet.",
			"To apply it: " + ApproveCommand(plan),
		},
	}
	return res
}

// ApproveCommand renders the exact command that applies a plan.
func ApproveCommand(plan *safety.Plan) string {
	cmd := "zabbix-ai-cli-mcp approve " + plan.ID
	if plan.RequiresConfirmName != "" {
		cmd += fmt.Sprintf(" --confirm %q", plan.RequiresConfirmName)
	}
	return cmd
}

// ApplyMode says how a change reached execution.
//
// It is a distinct type rather than a reading of the audit string, because the
// audit string exists to describe what happened and would then also be
// deciding what is permitted. The two jobs drift apart the moment a third
// caller is added.
type ApplyMode string

const (
	// ApplyDirect is a change applied by the same call that asked for it:
	// `--apply` at a terminal, or the MCP write tool.
	ApplyDirect ApplyMode = "direct"
	// ApplyStored is a plan that was stored first and approved afterwards.
	ApplyStored ApplyMode = "stored"
)

// ApplyOptions carry the authorisation for a change.
type ApplyOptions struct {
	// Mode says whether this is a direct application or an approved plan.
	Mode ApplyMode
	// Confirm is the exact object name echoed back for a destructive change.
	Confirm string
	// Approval records how the change was authorised, for the audit log.
	Approval safety.Approval
}

// CheckWriteAllowed refuses a direct application when configuration has not
// granted one.
//
// Approving a stored plan is never refused here: that path is a person at a
// terminal, and it is the path this error sends the caller to.
func CheckWriteAllowed(env *opspec.Env, mode ApplyMode) error {
	switch mode {
	case ApplyStored:
		return nil
	case ApplyDirect:
		if env.AllowWrite {
			return nil
		}
		return errs.New(errs.CodeWriteDisabled, errs.ExitPermission,
			"direct writes are disabled for profile %q, so this change was not made", env.Profile).
			WithSuggestion("approve the stored plan from a terminal, or set allow_write = true in the config file")
	default:
		// A caller that did not say how it was authorised has not been
		// authorised. Reading an unset mode as the permissive one is how a
		// gate quietly stops being a gate.
		return errs.Internal("a change was submitted without saying how it was authorised")
	}
}

// Apply executes a plan after checking every gate.
//
// The order matters: scope, then integrity, then expiry, then the echoed
// confirmation, and only then the preconditions against live Zabbix. Each
// check is cheap and each one refuses on its own.
func Apply(ctx context.Context, env *opspec.Env, plan *safety.Plan, opts ApplyOptions) (*output.Result, error) {
	op, ok := Lookup(planOperationName(plan))
	if !ok {
		return nil, errs.Internal("plan %s names an unknown operation %q", plan.ID, plan.Operation)
	}
	if err := CheckWriteAllowed(env, opts.Mode); err != nil {
		return nil, err
	}
	if err := CheckScope(env, op); err != nil {
		return nil, err
	}
	// A plan is a file, and the process that created it can rewrite it. Its
	// hash catches accidental corruption, not an author who recomputes it, so
	// risk and scope are derived again here from the code rather than read
	// back from the file. A plan that claims anything weaker than the registry
	// says is refused instead of applied.
	risk, scope, err := authoritativeRiskAndScope(op, plan)
	if err != nil {
		return nil, err
	}
	if plan.Risk != risk || plan.Scope != scope {
		return nil, errs.Denied(
			"plan %s claims to be a %s change needing %q, but %s is a %s change needing %q",
			plan.ID, plan.Risk, plan.Scope, plan.Operation, risk, scope).
			WithSuggestion("build the plan again rather than editing a stored one")
	}
	if err := CheckPlanScope(env, plan); err != nil {
		return nil, err
	}
	if plan.Profile != env.Profile {
		return nil, errs.Denied("plan %s was made against profile %q but the active profile is %q",
			plan.ID, plan.Profile, env.Profile).
			WithSuggestion("re-run with --profile %s, or create a new plan", plan.Profile)
	}
	if err := plan.Verify(time.Now()); err != nil {
		code := errs.CodePlanStale
		if plan.Expired(time.Now()) {
			code = errs.CodePlanExpired
		}
		return nil, errs.New(code, errs.ExitFailure, "%s", err.Error()).
			WithSuggestion("run the original command again to build a fresh plan")
	}
	// Echoing the target back is a guard against approving the wrong stored
	// plan minutes after reading it. A direct application has just been given
	// the target in its own arguments, so the echo would only be the caller
	// repeating itself.
	if plan.RequiresConfirmName != "" && opts.Mode != ApplyDirect && opts.Confirm != plan.RequiresConfirmName {
		return nil, errs.ApprovalRequired(
			"this change is destructive and needs the target named back exactly").
			WithSuggestion("run: %s", ApproveCommand(plan))
	}

	// Take exclusive ownership before anything reaches Zabbix, so two
	// concurrent approvals cannot both apply the same change.
	if env.Plans != nil {
		if err := env.Plans.Claim(plan.ID); err != nil {
			return nil, err
		}
	}

	result, applyErr := env.Service.ApplyPlan(ctx, plan)
	var postApplyWarnings []string
	if env.Audit != nil {
		entry := safety.AuditEntry{
			Profile:   env.Profile,
			Operation: plan.Operation,
			Risk:      plan.Risk,
			PlanID:    plan.ID,
			Approval:  opts.Approval,
			Resources: plan.Resources,
			Outcome:   "applied",
		}
		if redacted, ok := api.Redact(plan.Params).(map[string]any); ok {
			entry.Params = redacted
		}
		if applyErr != nil {
			entry.Outcome = "failed"
			entry.Error = applyErr.Error()
		}
		// A change that happened without a record of it having happened is
		// worse than a change that failed: the next person to look has no way
		// to tell. If the log cannot be written, say so where the caller will
		// see it rather than swallowing it.
		if auditErr := env.Audit.Append(entry); auditErr != nil {
			postApplyWarnings = append(postApplyWarnings,
				"the change attempt could not be written to the audit log: "+auditErr.Error())
		}
	}
	// The plan is discarded either way. A write that failed mid-flight may
	// still have reached Zabbix, so leaving it available to retry would invite
	// applying the same change twice.
	if env.Plans != nil {
		if discardErr := env.Plans.Discard(plan.ID); discardErr != nil {
			postApplyWarnings = append(postApplyWarnings,
				"the plan file could not be removed after applying: "+discardErr.Error())
		}
	}
	if applyErr != nil {
		if len(postApplyWarnings) > 0 {
			base := errs.FromAPI(applyErr)
			combined := *base
			combined.Message += "; " + strings.Join(postApplyWarnings, "; ")
			return nil, &combined
		}
		return nil, applyErr
	}

	res := &output.Result{Data: AppliedResult{
		Status:  StatusApplied,
		PlanID:  plan.ID,
		Summary: plan.Summary,
		Result:  result,
	}}
	res.Meta.Returned = 1
	res.Meta.Profile = env.Profile
	if len(postApplyWarnings) > 0 {
		res.Warnings = append(res.Warnings, postApplyWarnings...)
	}
	res.Table = &output.Table{
		Headers: []string{"RESULT"},
		Rows:    [][]string{{"applied: " + plan.Summary}},
	}
	return res, nil
}

// authoritativeRiskAndScope says what a plan's operation is actually allowed to
// do, according to the registry rather than according to the stored plan.
//
// The raw escape hatch is the interesting case: every method it can reach
// shares one registry entry, so the classification has to be recomputed from
// the method name the plan carries.
func authoritativeRiskAndScope(op *opspec.Operation, plan *safety.Plan) (safety.Risk, string, error) {
	if plan.Operation != "api.call" {
		return op.Risk, op.Scope, nil
	}
	method, _ := plan.Params["method"].(string)
	// The same gate the plan passed when it was written, applied again from
	// the stored params: a plan may have been sealed by a build whose registry
	// had no item type check, and it is this build that would run it.
	class := safety.ClassifyCall(method, plan.Params["params"])
	if !class.Allowed {
		reason := class.Reason
		if reason == "" {
			reason = "it is not in the risk registry"
		}
		return "", "", errs.Denied("plan %s calls %q, which is refused: %s", plan.ID, method, reason)
	}
	return class.Risk, class.Scope, nil
}

// planOperationName maps a plan back to its registry entry. The raw API
// escape hatch stores a single operation name for every method it can call.
func planOperationName(plan *safety.Plan) string {
	if plan.Operation == "api.call" {
		return "api.call"
	}
	// Plans use the same canonical names as the registry, except that the
	// registry groups trigger state changes under two commands.
	switch plan.Operation {
	case "trigger.enable":
		return "triggers.enable"
	case "trigger.disable":
		return "triggers.disable"
	case "event.acknowledge":
		return "events.acknowledge"
	}
	if strings.HasPrefix(plan.Operation, "maintenance.") {
		return plan.Operation
	}
	return plan.Operation
}
