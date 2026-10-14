package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/config"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/errs"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/mcp"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/opspec"
)

func mcpCommand(g *globals) *cobra.Command {
	var httpAddr, bearer string
	var readOnly, allowRemote bool
	var trustedOrigins []string

	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Serve the Model Context Protocol",
		Args:  cobra.NoArgs,
		Long: "Exposes the same operations the CLI runs as MCP tools, over stdio by default.\n\n" +
			"The Zabbix token stays inside this process; an MCP client never sees it. Whether a " +
			"client may change Zabbix follows allow_write in the config file: when it is on, " +
			"zabbix_write applies a change directly and every change is audited; when it is off, " +
			"a write request only produces a plan that a person approves with " +
			"'zabbix-ai-cli-mcp approve'. --read-only refuses both regardless.",
		RunE: func(cmd *cobra.Command, args []string) error {
			// The environment is rebuilt per call so a long-lived server picks
			// up a rotated token without being restarted.
			envFor := func(ctx context.Context) (*opspec.Env, error) {
				return g.buildEnv(ctx)
			}
			// Fail at startup rather than at the first tool call if the
			// profile is unusable. The environment is also where the write
			// setting is resolved, so this doubles as the snapshot the tool
			// list is built from.
			allowWrite, err := g.mcpStartup(cmd.Context(), httpAddr == "")
			if err != nil {
				return err
			}
			writable := allowWrite && !readOnly
			// An HTTP endpoint with no bearer token is open to every process
			// on the machine. That is a defensible default for a read-only
			// server and not for one that can change Zabbix, so this
			// combination is refused rather than warned about.
			if httpAddr != "" && writable && bearerToken(bearer) == "" {
				return errs.Denied(
					"serving HTTP with writes enabled and no bearer token would let any local process change Zabbix").
					WithSuggestion("set ZABBIX_AI_CLI_MCP_BEARER_TOKEN, or run with --read-only, or set allow_write = false")
			}
			server := mcp.NewServer(mcp.Options{
				Version:    Version,
				ReadOnly:   readOnly,
				AllowWrite: allowWrite,
				EnvFor:     envFor,
			})
			if httpAddr == "" {
				return mcp.ServeStdio(cmd.Context(), server)
			}
			fmt.Fprintf(g.stderr, "mcp: profile %s\n", g.profile)
			bearer = bearerToken(bearer)
			return mcp.ServeHTTP(cmd.Context(), server, mcp.HTTPOptions{
				Addr:             httpAddr,
				BearerToken:      bearer,
				AllowNonLoopback: allowRemote,
				TrustedOrigins:   trustedOrigins,
				Log:              g.stderr,
			})
		},
	}
	cmd.Flags().StringVar(&httpAddr, "http", "", "serve streamable HTTP on this address instead of stdio")
	cmd.Flags().StringVar(&bearer, "bearer-token", "",
		"require this bearer token from MCP clients; prefer ZABBIX_AI_CLI_MCP_BEARER_TOKEN, which is not visible in the process list")
	cmd.Flags().BoolVar(&readOnly, "read-only", false,
		"withhold the writing and planning tools, so a client cannot even describe a change")
	cmd.Flags().BoolVar(&allowRemote, "allow-remote", false,
		"permit binding an address that is not loopback")
	cmd.Flags().StringSliceVar(&trustedOrigins, "trusted-origin", nil,
		"browser origin permitted to call the HTTP endpoint")
	return cmd
}

// EnvIntrospection lets a stdio server with no Zabbix connection configured at
// all start anyway and answer initialize and tools/list. The container image
// sets it so that registries which build the image and inspect it, with no
// credentials and no network, can see the tools. It never supplies a URL or a
// token: every tool call still builds its environment and fails with the same
// "no Zabbix profile is configured" error the server would otherwise exit on.
const EnvIntrospection = "ZABBIX_AI_CLI_MCP_INTROSPECTION"

// mcpStartup checks the profile before serving and reports whether direct
// writes are allowed, which decides the tool list.
//
// It fails at startup rather than at the first tool call if the profile is
// unusable. The one exception is EnvIntrospection on stdio when nothing is
// configured: a URL or token that is present but wrong still stops the server.
func (g *globals) mcpStartup(ctx context.Context, stdio bool) (bool, error) {
	env, err := g.buildEnv(ctx)
	if err == nil {
		return env.AllowWrite, nil
	}
	var e *errs.E
	if !stdio || os.Getenv(EnvIntrospection) != "1" ||
		!errors.As(err, &e) || e.Code != errs.CodeNoProfile {
		return false, err
	}
	cfg, cerr := config.Load()
	if cerr != nil {
		return false, cerr
	}
	// A profile that is named but missing, or a token without a URL, is a
	// mistake rather than an empty installation, and still stops the server.
	if len(cfg.Profiles) > 0 || cfg.ActiveProfile != "" || g.profile != "" ||
		os.Getenv(config.EnvProfile) != "" ||
		os.Getenv(config.EnvToken) != "" || os.Getenv(config.EnvTokenFile) != "" {
		return false, err
	}
	allowWrite, werr := cfg.WriteAllowed(config.Profile{})
	if werr != nil {
		return false, werr
	}
	fmt.Fprintf(g.stderr, "mcp: no Zabbix profile is configured; serving the tool list only, "+
		"every tool call fails until %s and a token are set\n", config.EnvURL)
	return allowWrite, nil
}

// bearerToken prefers the environment over the flag. A flag value is visible to
// every process on the machine through the process list, and lands in shell
// history besides.
func bearerToken(flagValue string) string {
	if env := os.Getenv("ZABBIX_AI_CLI_MCP_BEARER_TOKEN"); env != "" {
		return env
	}
	return flagValue
}
