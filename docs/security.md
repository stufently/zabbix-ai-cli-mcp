# Security model

## Threat model

The distinguishing assumption is that the caller may be an AI agent acting on
instructions it read somewhere. Those instructions may come from a ticket, a log
line, or a Zabbix host name — all of which this program returns.

| Risk | Mitigation |
| --- | --- |
| An agent makes a destructive change it was talked into | `allow_write` decides. With it off, no MCP tool writes: a change becomes a plan a person applies at a terminal. With it on, the change is bounded by the profile's scopes and the risk registry, and lands in the audit log. |
| Prompt injection supplies a confirmation | No confirmation exists in the model's context to supply. Authorisation is configuration, read from the file or the environment; no MCP parameter carries it. |
| An agent acts on a stale plan | Parameters are hashed, plans expire after 15 minutes, and preconditions are re-read from Zabbix immediately before execution. |
| The token leaks into output or logs | The token is never printed, never logged, and never included in an error. Debug logging redacts request bodies. The MCP client never receives it. |
| Injection through Zabbix data | Returned strings are stripped of control characters and length-bounded. No policy decision reads their content. |
| An unclassified API method does something unexpected | The risk registry is an explicit table. A method absent from it is refused, not guessed at. |
| A single query exhausts the agent's context | Every list bounds itself and reports truncation. Field projections replace `output: extend`. |
| Shell injection | Nothing in this program builds a shell command. Parameters travel as JSON to the Go HTTP client. |
| An exposed MCP endpoint | HTTP binds loopback unless overridden, requires a bearer token off loopback, compares it in constant time, and applies cross-origin protection. |
| A downgraded TLS connection | Verification is on by default. A private CA is configured with `ca_file`; `insecure` exists but is never a default. |

## Why writes are permitted at all

Refusing writes outright is tempting and, in this case, was tried. What happened
was that the work got done anyway — with a token read out of a container's
environment, through a script that logged nothing. The refusal did not remove the
capability, only the record of it.

A permitted path that is bounded, classified and written to an audit log is a
better outcome than a refusal that gets routed around. Whether the path also
waits for a person is the `allow_write` setting, and the audit log is kept
either way.

## What is refused outright

Some methods are refused whatever a profile grants, because no diagnostic
workflow needs them and each one either hands out a credential, executes code, or
moves data that carries credentials:

`script.execute`, `task.create`, `user.login`, `token.*`, `configuration.export`,
`configuration.import`, `authentication.update`, `settings.update`,
`history.clear`, `usermacro.get`, and user, group, role and directory
administration.

Two of those deserve a note, because both read and a classifier that keys on the
verb would wave them through. `configuration.export` embeds macros in its output,
and `usermacro.get` returns them directly — macros are where Zabbix installations
keep database passwords and API keys, and this tool's output goes into a model's
context.

Writes are also refused, whatever scope a profile holds, for the objects whose
configuration is code or invokes code: `script`, `action`, `mediatype`,
`itemprototype`, `discoveryrule`, `hostprototype`, `httptest`, `webscenario`,
`connector`, `autoregistration`, `proxy` and `proxygroup`. Refusing
`script.execute` while allowing `script.create` plus `action.create` would only
lengthen the road to running a command on a monitored host, not close it. Reading
any of them stays available.

### Items are judged by their type

`item` is the exception, because the object is two different things under one
name: `item.create` is how a host gets an ordinary agent check, and also how a
script item that runs JavaScript on the Zabbix server appears. Refusing the
method took the ordinary case with it, and building a monitoring item is one of
the most common reasons to reach for the escape hatch at all.

So an item write is read before it is judged. These types are refused, because
collecting them runs something: external check (10), database monitor (11), SSH
agent (13), Telnet agent (14), HTTP agent (19), script (20) and browser (21).
Everything else — agent, agent (active), trapper, internal, simple check, SNMP,
calculated, dependent, JMX, IPMI — reads a value that something else already
produces, and is allowed under the `configuration` scope.

Three details follow from reading params rather than a method name, and each one
fails closed:

- **A write must state `type` explicitly.** On update Zabbix keeps the stored
  type, and for a script or SSH item the `params` field *is* its code — editing
  it without naming the type would be editing code sight unseen. An omitted type
  is refused rather than assumed, on create and on update alike.
- **`item.copy` is refused.** It duplicates items this call never names, so their
  types are not in the params to check: one script item copied to twenty hosts
  would be twenty new executions the gate never saw.
- **A batch is refused whole.** Params may be one object or a list; one
  executing type anywhere in the list refuses the call.

`item.delete` stays an ordinary destructive write: it carries no type and
executes nothing. `itemprototype` remains refused outright — a prototype's type
cannot be checked against the items discovery will later create from it.

## The write setting

`allow_write` decides whether a change may be applied by the call that asked for
it. It defaults to allowed. A profile's own `allow_write` overrides the
file-wide value, and `ZABBIX_AI_CLI_MCP_ALLOW_WRITE` overrides both — a
container is configured through its environment and a config file it may not
own. A value that is neither true nor false is an error rather than a guess.

With writes allowed, `zabbix_write` appears over MCP and an agent applies a
change itself. Everything else still applies: the profile's scopes, the risk
registry, the preconditions re-read from Zabbix, and the audit log, which
records whether a person or a model asked. Naming the target back is no longer
required for a destructive change applied directly — the caller supplied the
target in the same call — but it is still required to approve a stored plan,
where the point is that the plan is being read some time after it was written.

With writes disabled, nothing an MCP client can send applies a change: there is
no `apply` parameter, and a test fails the build if one appears. What the model
gets back is a plan and the command a person would run.

## What the gate does and does not protect

The gate is drawn against the model, not against the operating system.

That boundary is the OS user. An agent that also has a shell as the same user
can flip `allow_write` in the config file or run `--apply` itself — and could
equally read the token and call Zabbix directly, so neither the setting nor the
plan file is what is holding it back. For the same reason a stored plan's hash
is a check against corruption and stale reuse, not authentication: whoever can
rewrite the file can recompute the hash. That is why risk and scope are derived
again from the registry when a plan is applied, and a plan claiming anything
weaker than the code says is refused.

If an agent session on your machine should not be able to change Zabbix at all,
run the MCP server as a different user from the one holding a write-capable
token, with a profile whose scopes or `allow_write` say no. Separating those
users is the only arrangement in which the setting is genuinely out of reach.

An HTTP endpoint is open to every process on the machine unless it carries a
bearer token. That is defensible for a server that only reads, so a server that
can write refuses to start without one: set `ZABBIX_AI_CLI_MCP_BEARER_TOKEN`,
or run it `--read-only`.

## Layers

1. **The Zabbix token's own permissions.** The last real boundary. Give the token
   the least privilege the work needs; nothing here can widen it.
2. **Profile scopes.** A profile that names scopes is held to exactly those;
   without the matching scope, a write cannot even be planned. A profile that
   names none may do what the write setting allows.
3. **The risk registry.** An explicit table of what each operation may do.
4. **The write setting.** `allow_write = false` means nothing writes on the
   call that requested it; a person applies the stored plan at a terminal.
5. **The audit log.** Every applied change, with its plan, its parameters
   redacted of secrets, and whether a person or a model authorised it.

## Platform note

File permissions are the enforcement mechanism for the configuration, the
credentials file, stored plans and the audit log: directories `0700`, files
`0600`, with owner and symlink checks before a credential is read.

On Windows those mode bits are not enforced by the operating system, and Go's
`os.Chmod` cannot express an ACL. On a shared Windows machine, protect
`%AppData%\zabbix-ai-cli-mcp` through the filesystem ACL, or supply the token
through the environment and keep nothing on disk.

## Reporting a vulnerability

See [SECURITY.md](../SECURITY.md).
