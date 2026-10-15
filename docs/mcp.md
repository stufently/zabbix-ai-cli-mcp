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

## Claude Desktop

Download the `.mcpb` for your operating system from
<https://github.com/stufently/zabbix-ai-cli-mcp/releases/latest> and open it.
Claude Desktop installs the bundle and asks for three settings before the
server starts:

| Setting | Required | Passed to the server as |
| --- | --- | --- |
| Zabbix URL | yes | `ZABBIX_AI_CLI_MCP_URL` |
| API token | yes, masked and stored as a secret | `ZABBIX_AI_CLI_MCP_TOKEN` |
| Allow writes | no, off unless the user turns it on | `ZABBIX_AI_CLI_MCP_ALLOW_WRITE` |

The bundle is one binary for that platform. It does not need Node, Python or
Go. With writes left off, the extension is read-only: it is configured from the
URL and token alone, so it has no profile scopes, and `zabbix_plan_create`
refuses changes with `SCOPE_NOT_GRANTED`. To plan changes for a person to
approve, configure a profile with scopes by hand instead (see the README).
With writes on, `zabbix_write` is offered and every change is written to the
audit log. The extension talks to the Zabbix server named in the URL; it does
not send monitoring data anywhere else.

The same binary can be configured by hand in `claude_desktop_config.json`. See
the README for that block and for the other clients.

## Codex

```toml
# ~/.codex/config.toml
[mcp_servers.zabbix]
command = "zabbix-ai-cli-mcp"
args = ["mcp", "--profile", "prod"]
```

## Tools

| Tool | Purpose | Changes Zabbix | Listed when |
| --- | --- | --- | --- |
| `zabbix_problems` | Active problems, suppressed ones included and explained | no | always |
| `zabbix_problem` | One problem by event identifier | no | always |
| `zabbix_hosts` | Find hosts by fragment, pattern or group | no | always |
| `zabbix_host_status` | Compact operational state of one host | no | always |
| `zabbix_host_investigate` | Full diagnostic snapshot in one call | no | always |
| `zabbix_metrics_latest` | Newest value of a host's items | no | always |
| `zabbix_metrics_history` | Values over a window, with min, average and max | no | always |
| `zabbix_alert_why` | Why an event did or did not produce a notification | no | always |
| `zabbix_resolve` | A pasted notification to event, host and trigger identifiers | no | always |
| `zabbix_unreachable` | Monitored hosts Zabbix cannot poll | no | always |
| `zabbix_maintenance_list` | Maintenance windows, expired ones included | no | always |
| `zabbix_api_call` | Raw Zabbix API, read methods only | no | always |
| `zabbix_plan_create` | Describe a change without making it | no; stores a plan for a person to approve | not `--read-only` |
| `zabbix_plan_status` | Whether a plan is waiting, applied or gone | no | not `--read-only` |
| `zabbix_write` | Make a change; offered only where `allow_write` permits it | **yes** | `allow_write` is true and not `--read-only` |

`zabbix_write` is the only tool that changes Zabbix and the only one without
`readOnlyHint`. A test fails the build if any other tool loses that hint, if
`zabbix_write` is listed while writes are off, or if `--read-only` lists either
planning tool.

Each schema is generated from the operation registry, so it matches the CLI
exactly. An unknown parameter is refused with the list of accepted ones.

## Making a change

Whether a client may change Zabbix follows `allow_write`, which defaults to on.
See [the security model](security.md#the-write-setting) for where it is read
from and what it does not cover.

With writes allowed, `zabbix_write` takes the same arguments as
`zabbix_plan_create` and applies the change in one call:

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
    "status": "applied",
    "plan_id": "pl_cc89d2e87d15",
    "summary": "Create maintenance \"ms* (2h0m)\" for 2h0m",
    "result": {"maintenanceids": ["42"]}
  }
}
```

It still builds a plan first, so the risk class, the resolved hosts and the
preconditions re-read from Zabbix all apply; what it skips is the wait for a
person. The change is appended to the audit log, recorded as authorised by
`mcp-write` rather than by a person.

The tool list is fixed when the server starts, but the setting is read again on
every call: configuration tightened after startup is obeyed, and a stale tool
listing cannot widen anything.

## Media channels and lost LLD resources

These operations are available in both `zabbix_write` and `zabbix_plan_create`.
They require the `configuration` scope. Planning returns the usual
`approve_command`; direct writes and terminal approvals use the same audit log.

| Operation | Parameters | Effect |
| --- | --- | --- |
| `mediatypes.enable` | `mediatypeid` **or** exact `name` | Set only `status=0`, for any type |
| `mediatypes.disable` | `mediatypeid` **or** exact `name` | Set only `status=1`; destructive because alert delivery stops |
| `discoveryrules.set_lost_resources` | `itemid` **or** exact `template` + rule `name`, plus at least one lifecycle field | Change only the requested lost-resource policy fields |

```json
{"operation":"mediatypes.enable","params":{"mediatypeid":"1"}}
```

```json
{
  "operation": "discoveryrules.set_lost_resources",
  "params": {
    "template": "SystemD service monitoring template",
    "name": "Service Discovery",
    "enabled_lifetime_type": 2
  }
}
```

The media plan identifies the name, type, current and desired status. Disable
plans list users from `mediatype.get selectUsers` and actions from
`action.get mediatypeids` (including `0`, actions configured for all media).
The action list is conservative: all-media actions may use this channel. Counts and dependency lists reflect what the API user
can see. Repeating a media state request succeeds even if it is already in that
state. For a delayed restoration use an external scheduler with the CLI command
`zabbix-ai-cli-mcp mediatypes enable --mediatypeid 1 --apply` and a profile that
allows direct writes. For example, once the scheduler is configured, this cron
entry repeats the restoration daily at 03:30 in the scheduler's timezone:

```cron
30 3 * * * /absolute/path/zabbix-ai-cli-mcp mediatypes enable --mediatypeid 1 --apply
```

The MCP process need not stay alive. No timer is installed by these operations.
If direct writes are disabled, create a fresh plan and approve it at a terminal;
stored plans expire after 15 minutes, so do not schedule an old approval command.

LLD fields use the Zabbix names: `lifetime_type`, `lifetime`,
`enabled_lifetime_type`, `enabled_lifetime`. Each type accepts `0` (after the
corresponding lifetime), `1` (never), or `2` (immediately); numeric modes and
string-rendered integers are accepted. Lifetime values accept Zabbix durations,
seconds or user macros. Deletion mode `0` or `2`, and a deletion duration edited
without a mode, are classed as destructive. Immediate deletion removes item
history permanently. Disable-only edits leave deletion policy untouched.

The plan distinguishes host and template rules, counts unique linked hosts
through nested templates, and counts lost **items** belonging to those rules
using discovery status and scheduled disable/delete timestamps. Other discovered
resources (for example hosts created by host prototypes) are not part of that
item count. Effects occur when Zabbix processes discovery; this operation does
not execute discovery on demand. Returning disable mode to `1` does not re-enable
items that discovery already disabled.

Raw `api.call` accepts `mediatype.update` with only `mediatypeid` + `status`,
and `discoveryrule.update` with only `itemid` + the four lifecycle fields. These
exceptions are classified from their parameters; a method name alone still
receives the default refusal. They get the same impact plans as the domain
operations. An object or a non-empty array of objects is accepted, and **any**
extra field refuses the entire request, including `exec_path`, scripts,
parameters, SMTP settings, `key_`, filters, prototypes and intervals.

`api.call.params` may be an object/array or a JSON string, including through
`zabbix_api_call` for reads:

```json
{"operation":"api.call","params":{"method":"mediatype.update","params":{"mediatypeid":"1","status":"0"}}}
```

## Requesting a change instead

`zabbix_plan_create` records an intention without acting on it, and returns the
command a person runs. With `allow_write = false` it is the only route to a
change; with writes allowed it is still how an agent shows an operator what a
change would do before making it.

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

Relay `approve_command` verbatim. No parameter on this tool applies a plan, and
adding one would defeat the purpose: a confirmation an agent can send is a
confirmation prompt injection can send. Where a client is meant to apply
changes, that is what `zabbix_write` is for.

`--read-only` withholds both tools entirely, whatever the configuration says,
so a client cannot even describe a change.

## HTTP transport

stdio is the primary transport and the one to prefer: it needs no listening
socket, and it works in contexts where a user-scope HTTP configuration is not
read at all.

Where a long-lived shared endpoint is genuinely wanted:

```bash
zabbix-ai-cli-mcp mcp --profile prod --http 127.0.0.1:8000 --bearer-token "$MCP_TOKEN"
```

- Binds loopback; a routable address needs `--allow-remote` **and** a bearer token.
- A server that can write refuses to start without a bearer token, loopback
  included: without one, every process on the machine could change Zabbix
  through it. Run it `--read-only`, or set `allow_write = false`, if that is
  what you wanted.
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

Started with no Zabbix connection configured at all, the image still answers
`initialize` and `tools/list` over stdio, so registries can inspect it without
credentials; every tool call then fails with `NO_PROFILE`. The image sets
`ZABBIX_AI_CLI_MCP_INTROSPECTION=1` for this. Outside the image, or with any
URL, token or profile set, the server keeps failing at startup when the
configuration is unusable.

The image runs as a non-root user with a read-only root filesystem. For plans and
the audit log to survive, mount a writable state directory and point
`ZABBIX_AI_CLI_MCP_STATE_DIR` at it.
