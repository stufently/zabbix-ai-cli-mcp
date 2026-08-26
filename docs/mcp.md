# MCP server

```bash
zabbix-ai-cli-mcp mcp --profile prod
```

Speaks the Model Context Protocol over stdio. The Zabbix token is resolved inside
this process from the profile; an MCP client never sees it.

## Claude Code

```bash
claude mcp add zabbix -- zabbix-ai-cli-mcp mcp --profile prod
```

Several installations, several servers, no credentials in any client
configuration:

```bash
claude mcp add zabbix-prod  -- zabbix-ai-cli-mcp mcp --profile prod
claude mcp add zabbix-stage -- zabbix-ai-cli-mcp mcp --profile staging
```

## Codex

```toml
# ~/.codex/config.toml
[mcp_servers.zabbix]
command = "zabbix-ai-cli-mcp"
args = ["mcp", "--profile", "prod"]
```

## Tools

| Tool | Purpose |
| --- | --- |
| `zabbix_problems` | Active problems, suppressed ones included and explained |
| `zabbix_problem` | One problem by event identifier |
| `zabbix_hosts` | Find hosts by fragment, pattern or group |
| `zabbix_host_status` | Compact operational state of one host |
| `zabbix_host_investigate` | Full diagnostic snapshot in one call |
| `zabbix_metrics_latest` | Newest value of a host's items |
| `zabbix_metrics_history` | Values over a window, with min, average and max |
| `zabbix_alert_why` | Why an event did or did not produce a notification |
| `zabbix_resolve` | A pasted notification to event, host and trigger identifiers |
| `zabbix_unreachable` | Monitored hosts Zabbix cannot poll |
| `zabbix_maintenance_list` | Maintenance windows, expired ones included |
| `zabbix_api_call` | Raw Zabbix API, read methods only |
| `zabbix_plan_create` | Describe a change without making it |
| `zabbix_plan_status` | Whether a plan is waiting, applied or gone |

Each schema is generated from the operation registry, so it matches the CLI
exactly. An unknown parameter is refused with the list of accepted ones.

## Requesting a change

No tool writes. `zabbix_plan_create` records an intention and returns the command
a person runs:

```json
{
  "operation": "maintenance.create",
  "params": {"hosts": ["ms*"], "for": "2h"}
}
```

```json
{
  "ok": true,
  "data": {
    "status": "planned",
    "plan_id": "pl_cc89d2e87d15",
    "summary": "Create maintenance \"ms* (2h0m)\" for 2h0m",
    "impact_count": 14,
    "approve_command": "zabbix-ai-cli-mcp approve pl_cc89d2e87d15",
    "expires_at": "2026-08-21T05:54:22Z"
  }
}
```

Relay `approve_command` verbatim. There is no parameter that would let a client
apply it, and adding one would defeat the purpose: a confirmation an agent can
send is a confirmation prompt injection can send.

`--read-only` withholds the planning tools entirely, so a client cannot even
describe a change.

## HTTP transport

stdio is the primary transport and the one to prefer: it needs no listening
socket, and it works in contexts where a user-scope HTTP configuration is not
read at all.

Where a long-lived shared endpoint is genuinely wanted:

```bash
zabbix-ai-cli-mcp mcp --profile prod --http 127.0.0.1:8000 --bearer-token "$MCP_TOKEN"
```

- Binds loopback; a routable address needs `--allow-remote` **and** a bearer token.
- The bearer token authenticates MCP clients and is unrelated to the Zabbix token.
- `ZABBIX_AI_CLI_MCP_BEARER_TOKEN` supplies that bearer token without exposing it in
  the process list; `ZABBIX_AI_CLI_MCP_TOKEN` is the separate Zabbix API token.
- Compared in constant time.
- Wrapped in cross-origin protection, which the SDK does not apply by default.
- Explicit server timeouts and a 4 MiB body limit.

## Container

```bash
docker run --rm -i \
  -e ZABBIX_AI_CLI_MCP_URL=https://zabbix.example.com \
  -e ZABBIX_AI_CLI_MCP_TOKEN_FILE=/run/secrets/zabbix \
  -v /run/secrets/zabbix:/run/secrets/zabbix:ro \
  ghcr.io/stufently/zabbix-ai-cli-mcp mcp
```

The image runs as a non-root user with a read-only root filesystem. For plans and
the audit log to survive, mount a writable state directory and point
`ZABBIX_AI_CLI_MCP_STATE_DIR` at it.
