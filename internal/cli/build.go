package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/errs"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/ops"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/opspec"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/output"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/safety"
)

// operationCommands renders the whole registry as cobra commands.
func operationCommands(g *globals) []*cobra.Command {
	groups := map[string]*cobra.Command{}
	var top []*cobra.Command

	all := ops.All()
	// Group descriptions are taken from the first operation in each group, so
	// a new group needs no separate registration.
	for _, op := range all {
		cmd := operationCommand(g, op)
		if len(op.CLI) == 1 {
			top = append(top, cmd)
			continue
		}
		name := op.CLI[0]
		parent, ok := groups[name]
		if !ok {
			parent = &cobra.Command{
				Use:   name,
				Short: groupSummary(name),
			}
			groups[name] = parent
			top = append(top, parent)
		}
		parent.AddCommand(cmd)
	}
	sort.Slice(top, func(i, j int) bool { return top[i].Use < top[j].Use })
	return top
}

var groupSummaries = map[string]string{
	"host":        "Inspect hosts",
	"problems":    "Inspect active problems",
	"metrics":     "Read item values and history",
	"maintenance": "Inspect and manage maintenance windows",
	"triggers":    "Inspect and switch triggers",
	"events":      "Update events",
	"alert":       "Explain notification delivery",
	"api":         "Call the Zabbix API directly",
}

func groupSummary(name string) string {
	if s, ok := groupSummaries[name]; ok {
		return s
	}
	return name + " commands"
}

func operationCommand(g *globals, op *opspec.Operation) *cobra.Command {
	var positional []opspec.Param
	var flagged []opspec.Param
	for _, p := range op.Params {
		if p.Positional {
			positional = append(positional, p)
		} else {
			flagged = append(flagged, p)
		}
	}

	use := op.CLI[len(op.CLI)-1]
	for _, p := range positional {
		if p.Required {
			use += " <" + p.Name + ">"
		} else {
			use += " [" + p.Name + "]"
		}
	}

	cmd := &cobra.Command{
		Use:   use,
		Short: op.Summary,
		Long:  longHelp(op),
		Args:  cobra.MaximumNArgs(len(positional)),
	}

	values := map[string]any{}
	for _, p := range flagged {
		bindFlag(cmd, p, values)
	}
	// A positional parameter is also offered as a flag. Both spellings appear
	// in real use, and refusing one of them turns a working command into a
	// puzzle for no benefit.
	for _, p := range positional {
		bindFlag(cmd, p, values)
	}

	var apply bool
	var confirm string
	if op.Plan != nil {
		cmd.Flags().BoolVar(&apply, "apply", false,
			"make the change; without this the command only describes what it would do")
		cmd.Flags().StringVar(&confirm, "confirm", "",
			"name the target back exactly; required when approving a stored destructive plan, checked here if given")
	}

	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		input := map[string]any{}
		for i, p := range positional {
			if i < len(args) {
				input[p.Name] = args[i]
			}
		}
		for _, p := range append(append([]opspec.Param{}, flagged...), positional...) {
			if cmd.Flags().Changed(flagName(p.Name)) {
				input[p.Name] = dereference(values[p.Name])
			}
		}
		bound, err := op.Bind(input)
		if err != nil {
			return err
		}

		env, err := g.buildEnv(cmd.Context())
		if err != nil {
			return err
		}
		ctx := cmd.Context()

		if !op.Writes(bound) {
			res, err := ops.RunRead(ctx, env, op, bound)
			if err != nil {
				return err
			}
			return g.render(res)
		}

		plan, err := ops.CreatePlan(ctx, env, op, bound)
		if err != nil {
			return err
		}
		if !apply {
			return g.render(ops.PlanOutput(env, plan))
		}
		// --apply does not require the echo, having just been given the target
		// on the same command line. Passing one that disagrees is still a
		// mistake worth stopping on, and scripts written against the older
		// behaviour keep working.
		if confirm != "" && plan.RequiresConfirmName != "" && confirm != plan.RequiresConfirmName {
			return errs.ApprovalRequired("--confirm says %q but this change targets %q",
				confirm, plan.RequiresConfirmName)
		}
		res, err := ops.Apply(ctx, env, plan, ops.ApplyOptions{
			Mode:     ops.ApplyDirect,
			Confirm:  confirm,
			Approval: safety.ApprovalCLIApply,
		})
		if err != nil {
			return err
		}
		return g.render(res)
	}
	return cmd
}

func longHelp(op *opspec.Operation) string {
	var b strings.Builder
	b.WriteString(op.Summary)
	if op.Long != "" {
		b.WriteString("\n\n")
		b.WriteString(op.Long)
	}
	if op.Plan != nil {
		b.WriteString("\n\nThis command changes Zabbix. It describes the change and stops; " +
			"add --apply to make it, or approve the stored plan from a terminal.")
	}
	if op.Risk == safety.RiskDestructive {
		b.WriteString("\n\nIt is classed as destructive. Approving a stored plan for it needs " +
			"--confirm naming the target back; --apply does not, having just been given the target.")
	}
	return b.String()
}

// flagName renders a parameter name as a flag: parameters are snake_case in
// JSON, flags are kebab-case on the command line.
func flagName(param string) string { return strings.ReplaceAll(param, "_", "-") }

func bindFlag(cmd *cobra.Command, p opspec.Param, values map[string]any) {
	name := flagName(p.Name)
	usage := p.Description
	if p.Example != "" {
		usage += fmt.Sprintf(" (for example %s)", p.Example)
	}
	switch p.Type {
	case opspec.TypeInt:
		def, _ := p.Default.(int)
		v := cmd.Flags().Int(name, def, usage)
		values[p.Name] = v
	case opspec.TypeBool:
		def, _ := p.Default.(bool)
		v := cmd.Flags().Bool(name, def, usage)
		values[p.Name] = v
	case opspec.TypeStringList:
		def, _ := p.Default.([]string)
		v := cmd.Flags().StringSlice(name, def, usage)
		values[p.Name] = v
	default:
		def, _ := p.Default.(string)
		v := cmd.Flags().String(name, def, usage)
		values[p.Name] = v
	}
}

func dereference(v any) any {
	switch t := v.(type) {
	case *string:
		return *t
	case *int:
		return *t
	case *bool:
		return *t
	case *[]string:
		return *t
	default:
		return v
	}
}

// render writes a result in the caller's chosen format.
func (g *globals) render(res *output.Result) error {
	if g.resolveFormat() == output.FormatJSON {
		return output.WriteJSON(g.stdout, res)
	}
	return output.WriteTable(g.stdout, res)
}
