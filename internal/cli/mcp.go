package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
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
			"The Zabbix token stays inside this process; an MCP client never sees it. No tool can " +
			"change Zabbix: a write request produces a plan that a person approves with " +
			"'zabbix-ai-cli-mcp approve'.",
		RunE: func(cmd *cobra.Command, args []string) error {
			// The environment is rebuilt per call so a long-lived server picks
			// up a rotated token without being restarted.
			envFor := func(ctx context.Context) (*opspec.Env, error) {
				return g.buildEnv(ctx)
			}
			// Fail at startup rather than at the first tool call if the
			// profile is unusable.
			if _, err := envFor(cmd.Context()); err != nil {
				return err
			}
			server := mcp.NewServer(mcp.Options{
				Version:  Version,
				ReadOnly: readOnly,
				EnvFor:   envFor,
			})
			if httpAddr == "" {
				return mcp.ServeStdio(cmd.Context(), server)
			}
			fmt.Fprintf(g.stderr, "mcp: profile %s\n", g.profile)
			// A flag value is visible to every process on the machine through
			// the process list, and lands in shell history besides. The
			// environment is the safer place for a shared secret, so it wins.
			if env := os.Getenv("ZABBIX_AI_CLI_MCP_BEARER_TOKEN"); env != "" {
				bearer = env
			}
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
		"withhold the planning tool, so a client cannot even describe a change")
	cmd.Flags().BoolVar(&allowRemote, "allow-remote", false,
		"permit binding an address that is not loopback")
	cmd.Flags().StringSliceVar(&trustedOrigins, "trusted-origin", nil,
		"browser origin permitted to call the HTTP endpoint")
	return cmd
}
