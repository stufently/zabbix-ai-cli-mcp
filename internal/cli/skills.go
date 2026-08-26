package cli

import (
	"github.com/spf13/cobra"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/errs"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/output"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/skills"
)

func skillsCommand(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skills",
		Short: "Install the bundled agent skills",
		Long: "The skills describe workflows, not logic: they teach an agent which command to reach " +
			"for and how to read the answer. Claude Code and Codex both read skills/<name>/SKILL.md, " +
			"so one set of files serves both.",
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List the bundled skills",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			list, err := skills.List()
			if err != nil {
				return err
			}
			res := &output.Result{Data: list}
			res.Meta.Returned = len(list)
			rows := make([][]string, 0, len(list))
			for _, s := range list {
				rows = append(rows, []string{s.Name, s.Description})
			}
			res.Table = &output.Table{Headers: []string{"SKILL", "DESCRIPTION"}, Rows: rows}
			return g.render(res)
		},
	})

	var global, force bool
	install := &cobra.Command{
		Use:       "install <claude|codex>",
		Short:     "Copy the skills into an agent's skill directory",
		Args:      cobra.ExactArgs(1),
		ValidArgs: skills.Targets,
		RunE: func(cmd *cobra.Command, args []string) error {
			target := skills.Target(args[0])
			dest, err := skills.Destination(target, global)
			if err != nil {
				return err
			}
			results, err := skills.Install(dest, force)
			if err != nil {
				return err
			}
			skipped := 0
			rows := make([][]string, 0, len(results))
			for _, r := range results {
				if r.Status != "installed" {
					skipped++
				}
				rows = append(rows, []string{r.Name, r.Status, r.Path})
			}
			res := &output.Result{Data: map[string]any{
				"target": string(target), "destination": dest, "skills": results,
			}}
			res.Meta.Returned = len(results)
			res.Table = &output.Table{Headers: []string{"SKILL", "STATUS", "PATH"}, Rows: rows}
			if skipped > 0 {
				res.Warn("%d skill(s) were already present and left untouched; pass --force to overwrite", skipped)
			}
			return g.render(res)
		},
	}
	install.Flags().BoolVar(&global, "global", true, "install for the current user")
	install.Flags().BoolVar(&force, "force", false, "overwrite skills that are already installed")
	install.Flags().Bool("project", false, "install into the current directory instead of the user's home")
	install.PreRunE = func(cmd *cobra.Command, args []string) error {
		project, _ := cmd.Flags().GetBool("project")
		if project && cmd.Flags().Changed("global") && global {
			return errs.Usage("--global and --project cannot both be set")
		}
		if project {
			global = false
		}
		return nil
	}
	cmd.AddCommand(install)
	return cmd
}
