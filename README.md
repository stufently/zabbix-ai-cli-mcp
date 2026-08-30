# Zabbix AI CLI MCP — Zabbix MCP server and CLI for AI agents

[![CI](https://github.com/stufently/zabbix-ai-cli-mcp/actions/workflows/ci.yml/badge.svg)](https://github.com/stufently/zabbix-ai-cli-mcp/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/stufently/zabbix-ai-cli-mcp.svg)](https://pkg.go.dev/github.com/stufently/zabbix-ai-cli-mcp)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)
[![Zabbix](https://img.shields.io/badge/Zabbix-6.4%2B-red.svg)](#compatibility)
[![MCP](https://img.shields.io/badge/MCP-stdio%20%7C%20streamable%20HTTP-green.svg)](docs/mcp.md)

**`zabbix-ai-cli-mcp` is a Zabbix MCP server and command-line client in one Go
binary.** It gives Claude Code, Claude Desktop, Codex, Cursor and any other
Model Context Protocol client task-shaped access to Zabbix — what is broken
right now, why a host is silent, why an alert never arrived — with bounded
output, a stable JSON contract, and no change to Zabbix without a person's
approval.

It is for the people who get paged: SRE and DevOps teams running Zabbix who want
an agent to triage an incident without handing it the whole API.

**Requires Zabbix 6.4 or newer.** Bearer-token authentication arrived in 6.4 and
is the only scheme implemented; earlier versions expect the token in the request
body instead.

It never contacts a language model. The AI decides, this program executes,
Zabbix monitors.

```bash
zabbix-ai-cli-mcp login
zabbix-ai-cli-mcp problems list
zabbix-ai-cli-mcp host investigate server01
zabbix-ai-cli-mcp alert why 757474
zabbix-ai-cli-mcp mcp
```

## Why not another Zabbix API wrapper

Wrapping `host.get`, `problem.get` and `history.get` hands the agent the API's
sharp edges along with its power. On a current Zabbix 7.4 server:

- `problem.get` has no `selectHosts`, so a problem list arrives without hosts.
- `history.get` defaults to the numeric-unsigned table and returns **nothing** for
  a float item — silently, with no error.
- `item.lastvalue` and `item.lastclock` still exist and have returned a constant
  `"0"` for several major releases.
- `host.available` was removed in 5.4; availability lives on the interface.
- `event.acknowledge` takes a bitmask whose own documentation contradicts itself.
- `searchWildcardsEnabled: true` **disables** implicit substring matching, turning
  a name fragment into an exact match that quietly finds nothing.

Every one of those produces a confident, wrong answer rather than an error. This
tool absorbs them behind commands that describe the task instead of the endpoint.

## What it does

| Command | Answers |
| --- | --- |
| `problems list` | What is broken now — including suppressed problems, with the maintenance window that hides them named |
| `host investigate` | One call: host state, active problems, recent events, silent and unsupported items, maintenance |
| `host status` | A handful of fields instead of twelve thousand characters of configuration |
| `alert why` | Why a notification did or did not arrive — suppression, delivery attempts, actions, media types, per-recipient severity filters |
| `resolve` | Turns a notification pasted out of chat into event, host and trigger identifiers |
| `unreachable` | Monitored hosts Zabbix cannot poll, with the error it recorded |
| `metrics latest` / `history` | Values with the right history type, human units and `min/avg/max` |
| `maintenance` | Open, extend, end or remove windows, with host patterns like `ms*` |
| `api call` | The escape hatch, under the same rules |

## Safety

Read operations always run immediately. Whether anything else does is one
setting, `allow_write`, and it is **on by default**.

```toml
# ~/.config/zabbix-ai-cli-mcp/config.toml
allow_write = true            # the default; omit the key and you get this

[profiles.prod]
url = "https://zabbix.example.com"
allow_write = false           # this one profile is an exception
```

**If you are not sure, set `allow_write = false`.** It costs one command per
change and it is the right default for an installation you cannot afford to
have an agent surprise you in. A profile override beats the file-wide setting,
and `ZABBIX_AI_CLI_MCP_ALLOW_WRITE` beats both — that is how a container is
told, without owning the config file.

| Caller | `allow_write = true` | `allow_write = false` |
| --- | --- | --- |
| CLI, a person at a terminal | `--apply` makes the change | plan, then `approve` |
| MCP, an agent | `zabbix_write` makes the change | plan, then `approve` at a terminal |

With writes off, no tool and no flag applies anything: a change is described,
and a person runs `zabbix-ai-cli-mcp approve <plan-id>` in their own terminal.
A confirmation an agent could send would be a confirmation prompt injection
could send, so none is offered — the approval lives outside the model's context.

With writes on, the agent applies the change itself and every change lands in
an audit log with the profile, the parameters, the objects touched and whether
a person or a model asked for it. `zabbix-ai-cli-mcp mcp --read-only` refuses
both paths regardless of the setting, and an HTTP endpoint that can write
refuses to start without a bearer token.

Everything else holds either way: a profile's `scopes` still bound what it may
touch, the risk registry still refuses methods that hand out credentials or run
code, and a plan is still re-checked against live Zabbix before it executes.

```
$ zabbix-ai-cli-mcp maintenance create "ms*" --for 2h

PLAN pl_cc89d2e87d15

Create maintenance "ms* (2h0m)" for 2h0m, 2026-08-21T05:39:00Z to 2026-08-21T07:39:00Z

Affects:
  host ms1.8qw.ru
  host ms10.8qw.ru
  ...

Risk: write
Expires: 2026-08-21T05:54:22Z

Nothing has changed yet.
To apply it: zabbix-ai-cli-mcp approve pl_cc89d2e87d15
```

Before a plan runs, its parameters are re-hashed, its deadline checked and its
preconditions re-read from Zabbix. A window that has been replaced since the plan
was made is refused, not deleted. Every applied change is appended to an audit log.

This matters more than a refusal would. When the tool this replaces blocked a
write, the work was done anyway with a token copied out of a container — losing
the audit trail without preventing anything. A permitted path that is recorded
beats a refusal that gets routed around.

## Install

> Prebuilt archives and the `ghcr.io` image are published with each tagged
> release. Until the first tag lands, build from source with either method
> below.

For a host-native binary, use Go 1.25 or newer:

```bash
go install github.com/stufently/zabbix-ai-cli-mcp/cmd/zabbix-ai-cli-mcp@latest

# Or build the current checkout.
mkdir -p bin
go build -trimpath -o bin/zabbix-ai-cli-mcp ./cmd/zabbix-ai-cli-mcp
```

The Make targets are container-first and do not require Go on the host:

```bash
make build      # Linux binary in ./bin, built inside Docker
make docker     # Linux container image for the MCP server
```

## Configure

```bash
zabbix-ai-cli-mcp login --profile prod
```

It asks for the URL and the API token, verifies the token against the server, and
stores it. The token is never accepted as a flag, because flag values are visible
in shell history and in the process list; pipe it in instead:

```bash
printf %s "$TOKEN" | zabbix-ai-cli-mcp login --profile prod --url https://zabbix.example.com --token-stdin
```

A profile that names no scopes may do anything the write setting allows.
Naming any scope narrows it to exactly those:

```bash
zabbix-ai-cli-mcp profile scopes prod --add maintenance
zabbix-ai-cli-mcp profile show prod        # what it may do, writes included
```

See [docs/authentication.md](docs/authentication.md) for the resolution order and
the headless and container cases.

## Add the Zabbix MCP server to your AI client

The MCP client never sees the Zabbix token. It is resolved inside the server
process from the profile you configured, so the credential never enters a
model's context or a client's configuration file.

### Claude Code

```bash
claude mcp add zabbix -- zabbix-ai-cli-mcp mcp --profile prod
zabbix-ai-cli-mcp skills install claude
```

### Claude Desktop

`claude_desktop_config.json`:

```json
{
  "mcpServers": {
    "zabbix": {
      "command": "zabbix-ai-cli-mcp",
      "args": ["mcp", "--profile", "prod"]
    }
  }
}
```

### Codex

```toml
# ~/.codex/config.toml
[mcp_servers.zabbix]
command = "zabbix-ai-cli-mcp"
args = ["mcp", "--profile", "prod"]
```

```bash
zabbix-ai-cli-mcp skills install codex
```

### Cursor, Windsurf, VS Code and other MCP clients

Any client that speaks stdio takes the same two fields — command
`zabbix-ai-cli-mcp`, arguments `["mcp", "--profile", "prod"]`. For a client that
wants HTTP instead:

```bash
zabbix-ai-cli-mcp mcp --http 127.0.0.1:8000 --bearer-token "$MCP_TOKEN"
```

A server that can write refuses to start on HTTP without a bearer token, even on
loopback: without one, every process on the machine could change Zabbix through
it. It refuses a routable address unless you pass `--allow-remote` together with a
bearer token, because an unauthenticated MCP endpoint is an unauthenticated
route into Zabbix. See [docs/mcp.md](docs/mcp.md).

### Docker

```bash
docker run --rm -i \
  -e ZABBIX_AI_CLI_MCP_URL=https://zabbix.example.com \
  -e ZABBIX_AI_CLI_MCP_TOKEN_FILE=/run/secrets/zabbix \
  -v /path/to/token:/run/secrets/zabbix:ro \
  ghcr.io/stufently/zabbix-ai-cli-mcp:latest mcp
```

## MCP tools

Fifteen tools, not two hundred. A large tool surface costs an agent context
before it has done anything, and most of it is never called.

```
zabbix_problems           zabbix_metrics_latest      zabbix_unreachable
zabbix_problem            zabbix_metrics_history     zabbix_maintenance_list
zabbix_hosts              zabbix_alert_why           zabbix_api_call
zabbix_host_status        zabbix_resolve             zabbix_plan_create
zabbix_host_investigate                              zabbix_plan_status
                                                     zabbix_write
```

Write operations do not get one tool each. `zabbix_write` and
`zabbix_plan_create` take an `operation` enum generated from the same registry
the CLI is built from, so the tool surface does not grow as operations are
added. `zabbix_write` is offered only where `allow_write` permits it.

## JSON contract

```json
{
  "ok": true,
  "data": {},
  "warnings": [],
  "meta": {
    "returned": 50,
    "total": 381,
    "truncated": true,
    "truncated_reason": "row_limit",
    "partial": false,
    "zabbix_version": "7.4.10"
  }
}
```

Errors carry a stable code, whether retrying is worthwhile, and what to do next:

```json
{
  "ok": false,
  "error": {
    "code": "AUTHENTICATION_FAILED",
    "message": "Zabbix rejected the configured API token",
    "retryable": false,
    "suggestion": "run 'zabbix-ai-cli-mcp login' to configure a new token"
  }
}
```

`zabbix-ai-cli-mcp schema` prints every operation, its parameters and its JSON Schema,
so an agent can learn the tool programmatically instead of guessing at flags.

Full details in [docs/json-output.md](docs/json-output.md).

## Documentation

- [Authentication and profiles](docs/authentication.md)
- [Command line](docs/cli.md)
- [MCP server](docs/mcp.md)
- [Skills](docs/skills.md)
- [JSON output and exit codes](docs/json-output.md)
- [Security model](docs/security.md)
- [Architecture and design decisions](docs/architecture.md)

## FAQ

### What is a Zabbix MCP server?

An MCP server is a small program that exposes a system to an AI client over the
Model Context Protocol. A Zabbix MCP server lets Claude, Codex, Cursor and
similar clients query Zabbix — problems, hosts, items, events, maintenance — as
tools, instead of the model guessing at `curl` calls against the JSON-RPC API.

### Can an AI agent change my Zabbix through this?

That is yours to decide, and the setting is `allow_write`. Left alone it is on,
and an agent can open a maintenance window or acknowledge an event itself —
every change audited, and bounded by the profile's scopes and the risk
registry.

Set `allow_write = false` and it cannot. A write then produces a plan and
stops; applying it is a command you run in your own terminal,
`zabbix-ai-cli-mcp approve <plan-id>`. No MCP parameter applies anything in
that mode, and a test fails the build if one is ever added.

### Does it send my monitoring data to an AI provider?

No. This program never contacts a language model. It talks to Zabbix and prints
JSON. Whatever your MCP client does with that output is between you and your
client.

### Which Zabbix versions are supported?

Zabbix 6.4 and newer, because bearer-token authentication arrived in 6.4.
Developed and tested against Zabbix 7.4.

### Do I need Go installed?

No. `make build` compiles inside Docker and needs nothing on the host but
Docker itself. `go install` is there for a host-native binary, and each tagged
release publishes archives for Linux, macOS and Windows with checksums, along with a
container image on `ghcr.io`.

### How is this different from an MCP server that wraps the Zabbix API?

A thin wrapper hands the agent the API's sharp edges: `problem.get` without
hosts, `history.get` silently returning nothing for float items, `lastvalue`
frozen at `"0"`. Those produce confident wrong answers rather than errors. This
tool answers questions — "what is broken", "why did this alert not arrive" — and
absorbs the traps behind them. It also ships fifteen tools rather than two
hundred, because a large tool surface spends an agent's context before it does
any work.

### Can I still call the raw Zabbix API?

Yes, through `api call`, under the same rules as everything else. Methods that hand out
credentials or execute code are refused outright — including the long way round,
such as creating a script and having an action run it.

### Does it work without an AI client at all?

Yes. It is a normal CLI with human-readable tables, JSON output and documented
exit codes, so it is equally usable from a shell or a CI job.

## Compatibility

Zabbix 6.4 and newer. Bearer-token authentication arrived in 6.4 and is the only
scheme implemented. Version-dependent behaviour is asserted explicitly, so an
incompatibility is reported rather than returning an empty result.

Developed and tested against Zabbix 7.4.

## License

Apache-2.0. See [LICENSE](LICENSE).

Zabbix is a trademark of Zabbix LLC.
This project is an independent open-source project and is not affiliated with or
endorsed by Zabbix LLC.
