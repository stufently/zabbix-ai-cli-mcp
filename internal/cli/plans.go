package cli

import (
	"bufio"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/errs"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/ops"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/output"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/safety"
)

func plansCommand(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "plans",
		Short: "Inspect changes that have been described but not made",
		Long: "A plan is created whenever something asks for a change without authorising it, " +
			"which is what every request over MCP does. Plans expire after " + safety.DefaultTTL.String() + ".",
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List outstanding plans",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := planStore()
			if err != nil {
				return err
			}
			plans, err := store.List()
			if err != nil {
				return err
			}
			res := &output.Result{Data: plans}
			res.Meta.Returned = len(plans)
			rows := make([][]string, 0, len(plans))
			for _, p := range plans {
				rows = append(rows, []string{
					p.ID, p.Profile, string(p.Risk), p.Summary,
					time.Until(p.ExpiresAt).Round(time.Second).String(),
				})
			}
			res.Table = &output.Table{
				Headers: []string{"PLAN", "PROFILE", "RISK", "SUMMARY", "EXPIRES IN"},
				Rows:    rows,
			}
			return g.render(res)
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "show <plan-id>",
		Short: "Show one plan in full",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := planStore()
			if err != nil {
				return err
			}
			plan, err := store.Load(args[0])
			if err != nil {
				return err
			}
			res := &output.Result{Data: plan}
			res.Meta.Returned = 1
			res.Table = &output.Table{
				Headers: []string{"PLAN"},
				Rows:    [][]string{{plan.Describe()}},
				Notes:   []string{"To apply it: " + ops.ApproveCommand(plan)},
			}
			return g.render(res)
		},
	})
	return cmd
}

func approveCommand(g *globals) *cobra.Command {
	var confirm string
	var yes bool
	cmd := &cobra.Command{
		Use:   "approve <plan-id>",
		Short: "Apply a stored plan",
		Long: "This is the only way a change requested over MCP can happen. The approval is given " +
			"at a terminal by a person, so nothing inside a model's context can forge it.\n\n" +
			"The plan is shown and confirmed before anything runs.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := planStore()
			if err != nil {
				return err
			}
			plan, err := store.Load(args[0])
			if err != nil {
				return err
			}
			env, err := g.buildEnv(cmd.Context())
			if err != nil {
				return err
			}
			if plan.Profile != env.Profile {
				return errs.Denied("plan %s belongs to profile %q, but %q is active",
					plan.ID, plan.Profile, env.Profile).
					WithSuggestion("re-run with --profile %s", plan.Profile)
			}
			if plan.Expired(time.Now()) {
				_ = store.Delete(plan.ID)
				return errs.New(errs.CodePlanExpired, errs.ExitFailure,
					"plan %s expired at %s", plan.ID, plan.ExpiresAt.Format(time.RFC3339)).
					WithSuggestion("ask for the change again to get a fresh plan")
			}

			if !yes {
				ok, err := confirmInteractively(g, plan)
				if err != nil {
					return err
				}
				if !ok {
					_ = store.Delete(plan.ID)
					res := &output.Result{Data: map[string]any{"status": "rejected", "plan_id": plan.ID}}
					res.Meta.Returned = 1
					res.Table = &output.Table{Headers: []string{"RESULT"}, Rows: [][]string{{"rejected; nothing was changed"}}}
					return g.render(res)
				}
			}
			res, err := ops.Apply(cmd.Context(), env, plan, ops.ApplyOptions{
				Confirm:  confirm,
				Approval: safety.ApprovalTerminal,
			})
			if err != nil {
				return err
			}
			return g.render(res)
		},
	}
	cmd.Flags().StringVar(&confirm, "confirm", "", "name the target back exactly; required for destructive plans")
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the interactive prompt; required when stdin is not a terminal")
	return cmd
}

func rejectCommand(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "reject <plan-id>",
		Short: "Discard a plan without applying it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := planStore()
			if err != nil {
				return err
			}
			if _, err := store.Load(args[0]); err != nil {
				return err
			}
			if err := store.Delete(args[0]); err != nil {
				return err
			}
			res := &output.Result{Data: map[string]any{"status": "rejected", "plan_id": args[0]}}
			res.Meta.Returned = 1
			res.Table = &output.Table{Headers: []string{"RESULT"}, Rows: [][]string{{"discarded plan " + args[0]}}}
			return g.render(res)
		},
	}
}

// confirmInteractively shows the plan and waits for a person to agree.
func confirmInteractively(g *globals, plan *safety.Plan) (bool, error) {
	if !isTerminal(g.stdout) {
		return false, errs.ApprovalRequired(
			"approval needs a terminal so a person can read the plan first").
			WithSuggestion("re-run with --yes if this is a deliberate non-interactive approval")
	}
	fmt.Fprintln(g.stderr, plan.Describe())
	if plan.RequiresConfirmName != "" {
		fmt.Fprintf(g.stderr, "This is destructive. Target: %q\n", plan.RequiresConfirmName)
	}
	fmt.Fprint(g.stderr, "Apply this change? [y/N] ")
	line, err := bufio.NewReader(g.stdin).ReadString('\n')
	if err != nil && line == "" {
		return false, nil
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
}

func planStore() (*safety.Store, error) {
	dir, err := stateDir()
	if err != nil {
		return nil, err
	}
	return safety.NewStore(dir)
}
