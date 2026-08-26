# zabbix-ai-cli-mcp — Design

Date: 2026-08-21
Status: approved

## 1. Purpose

An AI-first CLI, MCP server and skill set for Zabbix, distributed as a single Go
binary. AI agents decide; the CLI executes; Zabbix monitors. The binary never
calls an LLM.

It replaces `mpeirone/zabbix-mcp-server` (Python, three generic passthrough
tools, whitelist-based safety, HTTP-only).

## 2. Evidence

Design decisions below trace to observed usage of the tool being replaced, not
to speculation.

| Observation | Consequence |
| --- | --- |
| 31 calls total, all via `zabbix_api`; `zabbix_api_docs` and `zabbix_api_list` never used | Do not ship documentation tools. Ship task-shaped operations. |
| 35% of calls returned `[]` | Fuzzy host/item lookup is a core feature, not a convenience. |
| 4 episodes of bypassing the MCP with direct JSON-RPC; `tmp/zbx_api.py` became a permanent tool | The tool must cover the task, or it will be routed around. |
| Whitelist refusal on 2026-08-10 did not stop the write — the agent read the token from the container `.env` and proceeded unaudited | A refusal without a permitted path removes observability, not capability. Writes need a gated path plus an audit log. |
| A dx4 outage stayed hidden for a month inside a suppression | Suppressed problems are shown by default, with the suppressing window named. |
| Answering "why was there no alert" took three ad-hoc scripts | `alert why` is a first-class operation. |
| One `history.get` returned 35,000 characters of raw data | Bounded output with explicit truncation metadata. |
| `host.get` returns 12k chars, `item.get` 15k | Field projections by default; never `output: extend`. |
| 2 of 9 failures were guessed parameter names (`selectHosts` on `problem.get`, `select_acknowledges`) | Validate parameters locally and name the permitted ones in the error. |
| HTTP user-scope config is not picked up in bot sessions or by subagents | stdio is the primary transport. |
| Zero requests for graphs, trends or reports | Not built. |

## 3. Architecture

```
                    internal/opspec (Operation, Param, Args, Risk)
                                     |
                    internal/ops  -- the registry: one Operation per task
                        /        \
             internal/cli      internal/mcp
            (cobra commands)   (MCP tools + schemas)
                        \        /
                    internal/service (domain use cases)
                           |
                    internal/api (thin JSON-RPC client)
                           |
                        Zabbix
```

Supporting packages: `internal/config` (TOML profiles, no secrets),
`internal/auth` (token resolution), `internal/safety` (risk registry, plans,
approval, audit), `internal/output` (envelope, projection, truncation),
`internal/zbx` (version and capability layer).

The `ops` registry is the single source of truth. CLI flags, MCP JSON schemas,
`zabbix-ai-cli-mcp schema` output and safety metadata are all derived from the same
`Operation` values, so the two front ends cannot drift apart. A test asserts
that every operation is reachable from both front ends or explicitly marked
otherwise.

## 4. Safety model

### Risk registry

Every operation declares `Risk` (`read`, `write`, `destructive`) and a `Scope`.
Classification is an explicit table, never a heuristic on the method suffix:
`script.execute`, `task.create`, `configuration.import` and `token.create` are
not ordinary writes. A raw API method absent from the table is **denied**, not
treated as destructive-with-a-plan.

### Scopes

A profile declares `scopes`. The default is `["read"]`. Creating a plan for a
write requires the matching scope (`acknowledge`, `maintenance`,
`configuration`) to be present in the active profile. This sits behind the
permissions of the Zabbix token itself, which remains the last real boundary.

### Execution paths

| Caller | read | write | destructive |
| --- | --- | --- | --- |
| CLI (human at a terminal) | immediate | plan, then `--apply` | plan, then `--apply --confirm <exact name>` |
| MCP (an LLM) | immediate | plan, then `approve` in a terminal | plan, then `approve` in a terminal |

MCP has no parameter that executes a write. A write tool returns a `plan_id`
and a human-readable diff; the agent asks the operator to run
`zabbix-ai-cli-mcp approve <plan_id>`. The approval secret never exists inside the
LLM's context, so prompt injection cannot forge it. This is the only mechanism
here that resists injection; a two-phase token inside the MCP channel would be
approved by the same model that requested it, and is therefore not used.

### Plans

A plan is a `0600` file under `$XDG_STATE_HOME/zabbix-ai-cli-mcp/plans/`. It holds
the canonical operation name, normalised parameters, resolved resource IDs, an
`impact_count`, preconditions, a parameter hash, and `expires_at` (15 minutes).
Before execution the preconditions are re-checked against live Zabbix; if the
state moved since planning, the plan is rejected rather than applied blind.

### Audit

Every executed write appends a JSON line to
`$XDG_STATE_HOME/zabbix-ai-cli-mcp/audit.log`: timestamp, profile, operation,
parameters, plan ID, approver path (`cli-apply` or `approve`), and the Zabbix
result. Reads are not audited.

### Untrusted data

Host names, problem texts and item values originate in Zabbix and may carry
prompt injection. They are returned as data, stripped of control characters and
length-bounded. No policy decision depends on their content.

## 5. Output contract

```json
{ "ok": true, "data": {}, "warnings": [],
  "meta": { "returned": 50, "total": 381, "truncated": true,
            "truncated_reason": "row_limit", "next_cursor": "...",
            "partial": false, "zabbix_version": "7.4.10" } }
```

Errors:

```json
{ "ok": false,
  "error": { "code": "HOST_NOT_FOUND", "message": "...",
             "retryable": false, "suggestion": "..." } }
```

Truncation is detected by requesting `limit+1`. Row, byte and field-length
limits apply independently. Serialised JSON is never cut mid-structure.
`partial: true` marks an aggregate where some sub-queries failed but the rest is
valid.

Exit codes: `0` success, `1` generic failure, `2` usage error,
`3` authentication, `4` API or connection, `5` not found, `6` permission denied,
`7` approval required, `8` unsupported Zabbix version.

Human output goes to stdout; diagnostics to stderr. `--json` and
`--output json|table` select the format; the default is a table on a TTY and
JSON otherwise, and the choice is always reported in `--debug`.

## 6. Zabbix 7.4 behaviour the service layer absorbs

Verified against a live 7.4.10 server:

- `problem.get` has no `selectHosts`. Hosts are resolved through a batched
  `trigger.get(triggerids, selectHosts)`.
- `history.get` defaults to history type `3` and silently returns nothing
  useful for float items. The service reads `item.value_type` first, groups
  item IDs by type, and issues one query per group.
- `item.lastvalue` and `item.lastclock` are accepted but always return `"0"`.
  Latest values come from `history.get` over a `2 x delay` window.
- `host.available` does not exist; availability is `interfaces[].available`
  plus `active_available`.
- `event.acknowledge.action` is a bitmask. The public surface is an enum
  (`ack`, `message`, `close`, `severity`, `suppress`, `unsuppress`); the mask is
  assembled internally and covered by tests.
- Maintenance windows require `timeperiods`; empty tags suppress every problem
  on the selected hosts; times round to minutes and recurring periods follow the
  Zabbix server timezone.
- No-data detection uses `delay`, `state`, `status` and maintenance-without-data
  collection, not `lastclock` alone.
- Aggregates cap concurrency at 4 and return partial results rather than failing
  wholesale.

## 7. Scope of v0.1

Read: `problems list|get`, `host list|get|status|investigate`,
`metrics latest|history`, `alert why`, `resolve`, `unreachable`, `api call`.

Write, each via plan then approval: `maintenance create|extend|expire|delete`,
`events acknowledge`, `triggers disable|enable`, `api call --apply`.

Support: `version`, `login`, `logout`, `auth status`,
`profile list|show|use|delete`, `plans list|show`, `approve`, `reject`,
`schema`, `mcp`, `skills list|install`.

MCP exposes fourteen tools: `zabbix_problems`, `zabbix_hosts`,
`zabbix_host_status`, `zabbix_host_investigate`, `zabbix_metrics_latest`,
`zabbix_metrics_history`, `zabbix_alert_why`, `zabbix_resolve`,
`zabbix_unreachable`, `zabbix_maintenance_list`, `zabbix_problem`,
`zabbix_api_call` (read-only), `zabbix_plan_create`, `zabbix_plan_status`. Write operations do not get one tool each:
`zabbix_plan_create` takes an `operation` enum generated from the registry, so
the tool surface does not grow as write operations are added.

Skills: `zabbix-status`, `zabbix-investigate`, `zabbix-maintenance`,
`zabbix-no-data`, `zabbix-alert-analysis`.

Deferred to v0.2: `flapping`, `host provenance`, `inventory diff`, web-scenario
management, host interface updates, `metrics top`, `templates`. One-off
configuration tasks are served by `api call` under the same plan-and-approve
rules. Graphs, trends and reports are not planned.

## 8. Credentials

Resolution order: `--token-stdin` > `ZABBIX_AI_CLI_MCP_TOKEN` >
`ZABBIX_AI_CLI_MCP_TOKEN_FILE` > OS keyring > credentials file. There is no silent
fallback from keyring to a plaintext file: a keyring failure reports the
available options and stops. The credentials file is an explicitly chosen
backend, written atomically, directory `0700` and file `0600`, with owner and
symlink checks. `--token` as a flag is not offered, because flags are visible in
shell history and the process list.

Config holds no secrets:

```toml
active_profile = "production"

[profiles.production]
url = "https://zabbix.example.com"
scopes = ["read", "maintenance"]
```

## 9. Transports

stdio is primary: `zabbix-ai-cli-mcp mcp --profile prod`. Streamable HTTP is
available via `zabbix-ai-cli-mcp mcp --http 127.0.0.1:8000`, binds to loopback,
requires a bearer token compared in constant time, and is wrapped in
`http.NewCrossOriginProtection` — the SDK applies no cross-origin protection by
default. Explicit server timeouts and body limits apply. The Zabbix token is
never visible to an MCP client.

## 10. Build and distribution

The official MCP Go SDK requires Go 1.25. The container-first Make targets use
`golang:1.27.0` and run with the invoking user's UID and GID so build artefacts
remain writable on the host. CI and release workflows install Go 1.27.0 on the
GitHub-hosted runner instead. The MCP image uses a multi-stage build and runs
the final distroless image as a non-root user; it is designed to work with a
read-only root filesystem and a writable state volume.

License: Apache-2.0.

## 11. Testing

`httptest`-backed fake Zabbix; table-driven service tests; golden-file tests for
the JSON envelope; CLI tests over the built binary. Required cases: the token
never reaches stdout, stderr or the audit log; a write never executes without
`--apply` or `approve`; an expired plan is rejected; a plan whose preconditions
moved is rejected; unknown API methods are denied; invalid profile; rejected
token; Zabbix API errors; network timeout; not found; truncated results;
acknowledge bitmask assembly; `history.get` type grouping. No live Zabbix server
is required.
