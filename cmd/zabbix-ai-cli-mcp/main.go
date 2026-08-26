// Command zabbix-ai-cli-mcp is an AI-first command line interface, MCP server and
// skill set for Zabbix.
//
// It never contacts a language model. An agent decides, this program executes,
// Zabbix monitors.
package main

import (
	"os"

	"github.com/stufently/zabbix-ai-cli-mcp/internal/cli"
)

func main() {
	os.Exit(cli.Execute(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
