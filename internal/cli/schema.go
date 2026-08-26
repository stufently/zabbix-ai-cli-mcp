package cli

import (
	"strings"

	"github.com/spf13/cobra"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/config"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/errs"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/ops"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/output"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/safety"
)

func stateDir() (string, error) { return config.StateDir() }

func schemaCommand(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "schema [command]",
		Short: "Describe the available operations as machine-readable JSON",
		Long: "Prints the same descriptions the MCP tools are generated from, so an agent can learn " +
			"this tool programmatically instead of guessing at flags.\n\n" +
			"With no argument it lists every operation. With one it describes that operation. " +
			"The special argument 'api-methods' lists the raw Zabbix methods the escape hatch will call.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 && args[0] == "api-methods" {
				methods := safety.KnownMethods()
				res := &output.Result{Data: map[string]any{"methods": methods}}
				res.Meta.Returned = len(methods)
				rows := make([][]string, 0, len(methods))
				for _, m := range methods {
					c := safety.ClassifyMethod(m)
					rows = append(rows, []string{m, string(c.Risk), c.Scope})
				}
				res.Table = &output.Table{Headers: []string{"METHOD", "RISK", "SCOPE"}, Rows: rows}
				return g.render(res)
			}
			if len(args) == 1 {
				op, ok := ops.LookupCommand(strings.Join(args, " "))
				if !ok {
					return errs.NotFound("no operation is called %q", args[0]).
						WithSuggestion("run 'zabbix-ai-cli-mcp schema' to list them")
				}
				res := &output.Result{Data: op.Describe()}
				res.Meta.Returned = 1
				return g.render(res)
			}

			all := ops.All()
			described := make([]map[string]any, 0, len(all))
			rows := make([][]string, 0, len(all))
			for _, op := range all {
				described = append(described, op.Describe())
				tool := op.MCPTool
				if tool == "" {
					tool = "-"
				}
				rows = append(rows, []string{op.CommandPath(), string(op.Risk), op.Scope, tool, op.Summary})
			}
			res := &output.Result{Data: map[string]any{
				"version":    Version,
				"operations": described,
			}}
			res.Meta.Returned = len(described)
			res.Table = &output.Table{
				Headers: []string{"COMMAND", "RISK", "SCOPE", "MCP TOOL", "SUMMARY"},
				Rows:    rows,
			}
			return g.render(res)
		},
	}
}
