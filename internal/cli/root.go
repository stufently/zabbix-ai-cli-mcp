// Package cli builds the command line from the operation registry.
//
// No command is hand-written twice: every flag, argument and help string comes
// from the same operation description the MCP server and the schema command
// read.
package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/api"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/auth"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/config"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/errs"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/opspec"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/output"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/safety"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/service"
)

// Version is stamped at build time by the release build.
//
// A binary from "go install" carries no ldflags, so it would report "dev" —
// which is the install path the README recommends first. The module version
// the toolchain recorded is used instead when nothing was stamped.
var Version = versionFromBuild()

func versionFromBuild() string {
	const unstamped = "dev"
	info, ok := debug.ReadBuildInfo()
	if !ok || info.Main.Version == "" || info.Main.Version == "(devel)" {
		return unstamped
	}
	return info.Main.Version
}

// globals holds the flags every command shares.
type globals struct {
	profile    string
	format     string
	json       bool
	verbose    bool
	debug      bool
	timeout    time.Duration
	tokenStdin bool

	stdout io.Writer
	stderr io.Writer
	stdin  io.Reader
}

func (g *globals) resolveFormat() output.Format {
	if g.json {
		return output.FormatJSON
	}
	switch g.format {
	case "json":
		return output.FormatJSON
	case "table":
		return output.FormatTable
	case "", "auto":
		if isTerminal(g.stdout) {
			return output.FormatTable
		}
		return output.FormatJSON
	default:
		return output.FormatTable
	}
}

func (g *globals) validate() error {
	switch g.format {
	case "", "auto", "json", "table":
	default:
		return errs.Usage("unknown output format %q; use auto, json or table", g.format)
	}
	if g.timeout < 0 {
		return errs.Usage("--timeout must not be negative")
	}
	return nil
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// buildEnv assembles everything an operation needs, including the credential.
func (g *globals) buildEnv(ctx context.Context) (*opspec.Env, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	name, profile, err := cfg.Resolve(g.profile)
	if err != nil {
		return nil, err
	}
	if profile.URL == "" {
		return nil, errs.New(errs.CodeNoProfile, errs.ExitAuth,
			"profile %q has no Zabbix URL", name).
			WithSuggestion("run 'zabbix-ai-cli-mcp login --profile %s'", name)
	}
	var stdinToken string
	if g.tokenStdin {
		stdinToken, err = auth.ReadTokenFromStdin(g.stdin)
		if err != nil {
			return nil, err
		}
	}
	token, err := auth.Resolve(name, profile, stdinToken)
	if err != nil {
		return nil, err
	}

	timeout := g.timeout
	if timeout <= 0 {
		if profile.TimeoutSeconds > 0 {
			timeout = time.Duration(profile.TimeoutSeconds) * time.Second
		} else {
			timeout = 30 * time.Second
		}
	}
	hc, err := httpClient(profile, timeout)
	if err != nil {
		return nil, err
	}
	opts := []api.Option{
		api.WithHTTPClient(hc),
		api.WithUserAgent("zabbix-ai-cli-mcp/" + Version),
	}
	if g.debug {
		opts = append(opts, api.WithLogger(func(format string, args ...any) {
			fmt.Fprintf(g.stderr, "debug: "+format+"\n", args...)
		}))
	}
	if g.verbose {
		fmt.Fprintf(g.stderr, "using profile %s (%s)\n", name, profile.URL)
	}
	client := api.New(profile.URL, token.Value, opts...)

	stateDir, err := config.StateDir()
	if err != nil {
		return nil, err
	}
	plans, err := safety.NewStore(stateDir)
	if err != nil {
		return nil, err
	}
	audit, err := safety.NewAuditLog(stateDir)
	if err != nil {
		return nil, err
	}
	return &opspec.Env{
		Service: service.New(client),
		Profile: name,
		Config:  profile,
		Plans:   plans,
		Audit:   audit,
	}, nil
}

func httpClient(p config.Profile, timeout time.Duration) (*http.Client, error) {
	transport, err := transportFor(p)
	if err != nil {
		return nil, err
	}
	return &http.Client{Timeout: timeout, Transport: transport}, nil
}

// Execute runs the command line and returns the process exit code.
func Execute(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	g := &globals{stdout: stdout, stderr: stderr, stdin: stdin}
	root := newRootCommand(g)
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	err := root.ExecuteContext(ctx)
	if err == nil {
		return errs.ExitOK
	}
	return reportError(g, err)
}

// reportError renders a failure in whichever format the caller asked for and
// maps it to an exit status.
func reportError(g *globals, err error) int {
	e := errs.FromAPI(err)
	if g.resolveFormat() == output.FormatJSON {
		_ = output.WriteErrorJSON(g.stdout, output.ErrorEnvelopeBody{
			Code:       e.Code,
			Message:    e.Message,
			Retryable:  e.Retryable,
			Suggestion: e.Suggestion,
		})
	} else {
		fmt.Fprintf(g.stderr, "error: %s\n", e.Message)
		if e.Suggestion != "" {
			fmt.Fprintf(g.stderr, "hint: %s\n", e.Suggestion)
		}
	}
	return errs.ExitCode(e)
}

func newRootCommand(g *globals) *cobra.Command {
	root := &cobra.Command{
		Use:   "zabbix-ai-cli-mcp",
		Short: "AI-first CLI, MCP server and skills for Zabbix",
		Long: "zabbix-ai-cli-mcp turns Zabbix into a small set of task-shaped commands that an AI agent " +
			"can be trusted to run: bounded output, stable JSON, and no change without approval.\n\n" +
			"The tool never contacts a language model. An agent decides, this program executes, Zabbix monitors.",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       Version,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			return g.validate()
		},
	}
	// Cobra reports a mistyped flag as a plain error, which would otherwise
	// surface as an internal failure with the wrong exit status.
	root.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return errs.Usage("%s", err.Error()).
			WithSuggestion("run '%s --help' to see the accepted flags", cmd.CommandPath())
	})

	pf := root.PersistentFlags()
	pf.StringVar(&g.profile, "profile", "", "profile to use (default: the active profile)")
	pf.StringVar(&g.format, "output", "auto", "output format: json, table or auto")
	pf.BoolVar(&g.json, "json", false, "shorthand for --output json")
	pf.BoolVar(&g.verbose, "verbose", false, "explain what is happening on stderr")
	pf.BoolVar(&g.debug, "debug", false, "log API calls on stderr with credentials redacted")
	pf.DurationVar(&g.timeout, "timeout", 0, "bound a single API call (default 30s)")
	pf.BoolVar(&g.tokenStdin, "token-stdin", false, "read the API token from stdin")

	root.AddCommand(operationCommands(g)...)
	root.AddCommand(
		versionCommand(g),
		loginCommand(g),
		logoutCommand(g),
		authCommand(g),
		profileCommand(g),
		plansCommand(g),
		approveCommand(g),
		rejectCommand(g),
		schemaCommand(g),
		mcpCommand(g),
		skillsCommand(g),
	)
	// A command that only groups subcommands has no Run of its own, and cobra
	// answers "plans reject x" by printing help and exiting 0. A caller
	// reading exit codes cannot tell that from success, so every group refuses
	// a name it does not have. An argument that fails validation is a usage
	// error too, not the internal error it would otherwise be reported as.
	for _, sub := range root.Commands() {
		requireSubcommand(sub)
	}
	usageArgErrors(root)
	return root
}

func requireSubcommand(cmd *cobra.Command) {
	for _, sub := range cmd.Commands() {
		requireSubcommand(sub)
	}
	if !cmd.HasSubCommands() || cmd.Run != nil || cmd.RunE != nil {
		return
	}
	names := make([]string, 0, len(cmd.Commands()))
	for _, sub := range cmd.Commands() {
		names = append(names, sub.Name())
	}
	sort.Strings(names)
	available := strings.Join(names, ", ")
	cmd.RunE = func(c *cobra.Command, args []string) error {
		if len(args) > 0 {
			return errs.Usage("%s has no subcommand %q", c.CommandPath(), args[0]).
				WithSuggestion("available: %s", available)
		}
		return errs.Usage("%s needs a subcommand", c.CommandPath()).
			WithSuggestion("available: %s", available)
	}
}

func usageArgErrors(cmd *cobra.Command) {
	for _, sub := range cmd.Commands() {
		usageArgErrors(sub)
	}
	if cmd.Args == nil {
		return
	}
	inner := cmd.Args
	cmd.Args = func(c *cobra.Command, args []string) error {
		if err := inner(c, args); err != nil {
			return errs.Usage("%s: %s", c.CommandPath(), err.Error())
		}
		return nil
	}
}

func versionCommand(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if g.resolveFormat() == output.FormatJSON {
				res := &output.Result{Data: map[string]string{"version": Version}}
				res.Meta.Returned = 1
				return output.WriteJSON(g.stdout, res)
			}
			fmt.Fprintf(g.stdout, "zabbix-ai-cli-mcp %s\n", Version)
			return nil
		},
	}
}
