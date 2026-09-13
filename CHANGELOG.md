# Changelog

## [Unreleased]

### Changed

- Item writes are judged by the item's `type` instead of being refused by method
  name. `item.create`, `item.update`, `item.massupdate` and `item.delete` now run
  under the `configuration` scope, while the types whose collection executes
  something stay refused: external check (10), database monitor (11), SSH agent
  (13), Telnet agent (14), HTTP agent (19), script (20) and browser (21).

  The blanket refusal was too coarse in both directions: it stopped an ordinary
  agent check — the most common reason to reach for the escape hatch — while
  leaving the reason for it, that *some* item types run code, unexpressed in the
  gate itself. The new gate reads the params it is given, and fails closed three
  ways: a write must state `type` explicitly (on update Zabbix keeps the stored
  type, and for a script item the `params` field is its code), `item.copy` is
  refused because the items it duplicates are not named in the params, and one
  executing type anywhere in a batch refuses the whole batch. `itemprototype`
  remains refused outright, since a prototype's type says nothing about the items
  discovery will create from it.

## [0.3.0] — 2026-08-30

### Added

- `allow_write`, one setting that decides whether a change may be applied by the
  call that asked for it. It goes in the config file, either at the top level or
  inside a `[profiles.x]` block, and `ZABBIX_AI_CLI_MCP_ALLOW_WRITE` overrides
  both. **It defaults to on.** Set it to `false` for the previous behaviour,
  where every change waits for `zabbix-ai-cli-mcp approve` at a terminal.
- `zabbix_write`, an MCP tool that applies a change in one call. It takes the
  same `operation` and `params` as `zabbix_plan_create`, is offered only where
  `allow_write` permits it, and re-reads the setting at execution so that
  configuration tightened after the server started is still obeyed. The change
  is audited as authorised by `mcp-write`, distinct from a person running
  `--apply`.
- `WRITE_DISABLED`, the error code for a direct write the configuration forbids.
  It names the approval path rather than failing blankly.
- `profile show`, `profile list`, `auth status` and `login` now report whether
  direct writes are allowed.

### Changed

- **Breaking:** a profile that names no `scopes` is no longer read-only. While
  `allow_write` is on — the default — it grants every scope, because a default
  of "allowed" that permits nothing reads as broken. A profile that names any
  scope is still held to exactly those, so pin a profile down by listing its
  scopes, or set `allow_write = false`. Existing installations that relied on an
  empty `scopes` list to keep a profile read-only must now say so explicitly.
- `--apply` no longer requires `--confirm` for a destructive change: the target
  was named on the same command line, so echoing it back was the caller
  repeating itself. Approving a *stored* destructive plan still requires it,
  because that plan is read some time after it was written.
- `profile scopes --remove` on a profile that named no scopes now writes the
  inherited set down before removing, so the removal narrows the profile instead
  of silently doing nothing. Removing the last scope leaves `scopes = ["read"]`
  rather than an empty list, which would have read as "unstated" and inherited
  everything back.
- `--confirm` is still checked when given alongside `--apply`: it is no longer
  required, but one that names a different target refuses rather than being
  ignored, so scripts written against the older behaviour keep their guard.
- Logging in again preserves a profile's `allow_write`, rather than dropping it
  as a side effect of rotating a token.
- The MCP server's instructions, tool descriptions and the `zabbix-ai-cli-mcp`
  help text no longer claim that nothing here can change Zabbix. What they say
  now follows the mode the server is actually running in.

### Security

- An MCP server that can write refuses to start on HTTP without a bearer token,
  loopback included. Without one, every process on the machine could change
  Zabbix through it — defensible for a read-only endpoint, not for this. Run it
  `--read-only`, or set `allow_write = false`, if that was the intent.
- `--read-only` now withholds `zabbix_write` as well as the planning tools, and
  overrides the configuration in both directions.
- An unreadable `ZABBIX_AI_CLI_MCP_ALLOW_WRITE` is an error rather than a guess:
  a security question is not settled by interpreting "maybe". It is read before
  `login` stores anything, so a bad value cannot leave a profile half-written.
- Applying a change without stating how it was authorised is refused. An unset
  mode used to read as the permissive one, which is how a gate quietly stops
  being a gate.

## [0.2.0] — 2026-08-26

### Changed

- **Breaking rename:** the project and Go module are now
  `github.com/stufently/zabbix-ai-cli-mcp`. Users must update installations,
  scripts and MCP client configurations from the `zabbix-ai-cli` binary to
  `zabbix-ai-cli-mcp`.
- Environment variables now use the `ZABBIX_AI_CLI_MCP_` prefix. In particular,
  migrate `ZABBIX_AI_CLI_URL`, `ZABBIX_AI_CLI_TOKEN`,
  `ZABBIX_AI_CLI_TOKEN_FILE`, `ZABBIX_AI_CLI_PROFILE`,
  `ZABBIX_AI_CLI_CONFIG_DIR` and `ZABBIX_AI_CLI_STATE_DIR` to
  `ZABBIX_AI_CLI_MCP_URL`, `ZABBIX_AI_CLI_MCP_TOKEN`,
  `ZABBIX_AI_CLI_MCP_TOKEN_FILE`, `ZABBIX_AI_CLI_MCP_PROFILE`,
  `ZABBIX_AI_CLI_MCP_CONFIG_DIR` and `ZABBIX_AI_CLI_MCP_STATE_DIR`. Note that
  `ZABBIX_AI_CLI_MCP_TOKEN` now means the Zabbix API token: the HTTP bearer
  token that used to carry that name moves to `ZABBIX_AI_CLI_MCP_BEARER_TOKEN`,
  which says what it is instead of colliding with the new prefix.
- Move persisted configuration and state from `~/.config/zabbix-ai-cli` to
  `~/.config/zabbix-ai-cli-mcp`, from `~/.local/state/zabbix-ai-cli` to
  `~/.local/state/zabbix-ai-cli-mcp`, and, for containers, from
  `/var/lib/zabbix-ai-cli` to `/var/lib/zabbix-ai-cli-mcp`.
- Pull and repin containers from `ghcr.io/stufently/zabbix-ai-cli-mcp` instead of
  `ghcr.io/stufently/zabbix-ai-cli`, and update MCP registry references from
  `io.github.stufently/zabbix-ai-cli` to
  `io.github.stufently/zabbix-ai-cli-mcp`.

## [0.1.1] — 2026-08-21

### Fixed

- A method the registry refuses outright was reported over MCP as "api.call is
  a write and cannot run through this tool", with a suggestion to plan it
  instead — and the planning tool then refused it for the real reason. Found by
  calling `usermacro.get` against the running server. A refusal now explains
  itself the first time.
- A binary from `go install` reported its version as `dev`, because ldflags are
  only applied by the release build — and `go install` is the first install
  method the README offers. It now falls back to the module version the Go
  toolchain recorded.

## [0.1.0] — 2026-08-21

### Added

- 2026-08-21 — First working version.
  - Single Go binary providing a CLI, an MCP server and five agent skills over one
    operation registry, so the command line and the MCP tools cannot diverge.
  - Read commands: `problems list|get`, `host list|get|status|investigate`,
    `metrics latest|history`, `triggers list`, `maintenance list`, `unreachable`,
    `alert why`, `resolve`, `api call`.
  - Write operations under plan and approval: `maintenance create|extend|expire|delete`,
    `events acknowledge`, `triggers disable|enable`, `api call --apply`.
  - Safety model: an explicit risk registry, profile scopes, plans with hashed
    parameters, a fifteen-minute deadline, preconditions re-checked against live
    Zabbix, terminal-only approval for anything requested over MCP, and an audit
    log of every applied change.
  - Thirteen MCP tools over stdio and streamable HTTP, with cross-origin
    protection and constant-time bearer authentication on the HTTP transport.
  - Stable JSON envelope with truncation and partial-result metadata, nine
    documented exit codes, and `schema` generated from the same registry.
  - Credential resolution across stdin, environment, token file, OS keyring and a
    credentials file, with no silent downgrade from the keyring to plain text.

### Security

Found by independent review of the finished implementation and fixed before release:

- A plan's fingerprint covered only its operation and parameters, leaving the
  deadline, risk class, required confirmation, preconditions and summary
  editable on disk. Plans are files owned by the same user an agent runs as, so
  the whole plan is now fingerprinted — including the summary, because that is
  the text a person reads before approving.
- `api call` declares itself a read, because whether it writes depends on the
  method it is handed. The scope check keyed on that declaration, so a
  read-only profile could plan and apply `maintenance.delete` through the escape
  hatch. Scope is now checked against the plan that was actually built.
- Two concurrent approvals of the same plan could both reach Zabbix. A plan is
  now claimed by an atomic rename before anything is sent, and discarded
  afterwards whether or not the change succeeded.
- `usermacro.get` is refused. `configuration.export` was already refused because
  its output embeds macros; reading those macros directly is the same secrets by
  another route, and this tool's output lands in a model's context.
- Writes through `api call` are refused for every object whose configuration is
  code or invokes code: `script`, `action`, `mediatype`, `item`, `itemprototype`,
  `discoveryrule`, `hostprototype`, `httptest`, `webscenario`, `connector`,
  `autoregistration`, `proxy` and `proxygroup`. Refusing `script.execute` while
  allowing a script to be created and an action told to run it only lengthened
  the road to running a command on a monitored host. Reading them is unchanged.
- `--http :8000` was treated as loopback because its host part is empty, so it
  bound every interface without requiring `--allow-remote` or a bearer token. An
  empty host now counts as remote, and a host name is loopback only when every
  address it resolves to is.
- A plan's risk and scope were read back from the stored file. They are now
  derived again from the registry when the plan is applied — for `api call`,
  from the method the plan carries — and a plan claiming anything weaker than
  the code says is refused. The hash was never authentication: whoever can
  rewrite the file can recompute it.
- The MCP bearer token can be supplied through `ZABBIX_AI_CLI_MCP_TOKEN`. Passed
  as a flag it is visible to every process on the machine.
- Redaction now covers `passwd`, SNMP community strings and several other
  credential-bearing field names.
- The maintenance skill told the agent reading it that it could apply a plan
  itself with `--apply`. It now says plainly that it cannot.

### Added

- Publishing to the official MCP registry as `io.github.stufently/zabbix-ai-cli-mcp`,
  generated by GoReleaser at release time and authenticated with a short-lived
  GitHub OIDC token rather than a stored credential. The container image carries
  the matching `io.modelcontextprotocol.server.name` label the registry checks.
- README: install snippets for Claude Code, Claude Desktop, Codex, Docker and any
  stdio client, plus a FAQ answering what a Zabbix MCP server is, whether an agent
  can change Zabbix through it, and whether monitoring data reaches a model.

### Fixed

Found by review of the Go 1.27 hardening pass:

- `plans list` failed outright if any one plan was claimed, discarded or expired
  between reading the directory and reading that plan — an operator approving a
  plan in a terminal made every other outstanding plan invisible to a concurrent
  reader. A plan vanishing under the listing is now skipped; a corrupt file
  still stops it.
- `zabbix_plan_status` reported "applying — check again for its audited outcome"
  for up to two full plan lifetimes when discarding a claim failed after the
  change was applied. The audit log now wins over a leftover claim.
- `alert why` resolved recipients with two serial `user.get` calls per candidate
  and no reuse, so an installation with a few dozen notifying actions ran past
  the client timeout and answered nothing. Identical lookups are now made once.
- `read_error` carried Zabbix-controlled text into JSON and into a table cell
  without sanitising, unlike every other API-sourced string. Control characters
  and escapes reached the terminal and the field-length bound was bypassed.
- A host whose visible name is a long numeric asset tag stopped resolving: the
  ID fast path returned Zabbix's refusal instead of falling back to the name
  search.
- `login --store <invalid> --token-stdin` consumed the piped token before
  validating the flag, so the secret was gone and had to be piped again.
- `make fmt-check` failed on the Go toolchain's own test data, and `make fmt`
  would have rewritten it: the build cache lived inside the repository, which is
  bind-mounted into the container as the source tree. The cache moved out.
- The `go` directive went back to 1.25.0. It is a lower bound on consumers, not
  a build pin — verified: the module builds unchanged on a 1.25 toolchain with
  `GOTOOLCHAIN=local`. Dockerfile, Makefile and CI stay pinned to 1.27.0.

Behaviours found against a live Zabbix 7.4.10 server, each of which produces a
plausible wrong answer rather than an error:

- `searchWildcardsEnabled: true` disables implicit substring matching, so name
  fragments matched nothing. Wildcards are now enabled only when the pattern
  contains one.
- `history.get` defaults to the numeric-unsigned table and returns nothing for a
  float item. Value types are read first and item IDs grouped per type.
- `item.lastvalue` and `item.lastclock` return a constant `"0"`. Latest values
  come from history.
- `problem.get` has no `selectHosts`. Hosts are resolved through a batched
  `trigger.get`.
- `host.available` no longer exists; availability is read per interface, plus
  `active_available`.
- `apiinfo.version` is rejected when an Authorization header is present.
- `event.acknowledge` takes a bitmask whose documentation contradicts its own
  example. Operations are named and the mask assembled internally.

Also found by review:

- `maintenance expire` on a window that had not yet started moved its end to
  five minutes after a start still in the future, which looked like a
  cancellation and was not one. It is now refused, pointing at `delete`.
- A host group named exactly `Linux` silently widened into every group whose
  name contained it. An exact name now wins outright.
- No-data detection sampled the first forty items, which are alphabetical. The
  sample is now spread across the whole list by proportion; the first attempt
  used a whole-number stride, which rounds down to 1 for anything under eighty
  items and quietly became "the first forty" again.
- The exact-name rule for host groups reached only the read paths. `maintenance
  create --groups Linux` still silenced every group containing the word — the
  write path, and the consequential one. Both now share one resolver, which
  looks the exact name up in its own query so a capped substring search cannot
  hide it.
- A plan claimed by an applier reported "no plan exists" to anything that looked
  at it afterwards, rather than saying it was being applied. Claims left behind
  by a killed process are now pruned.
- A negative `limit` meant "no limit". Integer parameters now carry a range,
  publish it in the MCP schema, and refuse a fractional value instead of
  truncating it.
- A failure to write the audit log after an applied change was swallowed. It is
  now reported as a warning on the result.
- An unknown subcommand printed help and exited 0, which anything reading exit
  codes reads as success — `plans reject x` looked like it had rejected
  something. A command group now refuses a name it does not have, and a
  positional argument that fails validation is a usage error rather than an
  internal one.
- An item whose history could not be read was reported as having no data. In
  this tool "no data" reads as "your monitoring is broken", so a database error
  looked like a dead agent. A failed read now carries `read_error`, the items
  that did answer are still returned, and an investigation warns that its
  no-data count understates.
- `maintenance expire` on a window that started moments ago said alerting
  resumes now, when Zabbix will not end a window shorter than its minimum
  period. The summary says when it actually resumes.
- The container could not start: distroless runs as uid 65532 and
  `/var/lib/zabbix-ai-cli-mcp` did not exist. It is created owned by that user, and
  CI now runs the image to check.
- An item with `units: unixtime` and no data rendered as `1970-01-01`.
