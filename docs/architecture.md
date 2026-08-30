# Architecture and design decisions

## Shape

```
                    internal/opspec  — Operation, Param, Args, Risk
                                |
                    internal/ops     — the registry: one Operation per task
                        /        \
             internal/cli      internal/mcp
            (cobra commands)   (MCP tools and JSON Schemas)
                        \        /
                    internal/service — domain use cases
                           |
                    internal/api     — thin JSON-RPC client
                           |
                        Zabbix
```

Supporting packages: `config` (TOML profiles, no secrets), `auth` (token
resolution), `safety` (risk registry, plans, approval, audit), `output` (envelope,
projection, truncation), `zbx` (version and capability layer), `skills` (embedded
skill files), `errs` (the error contract).

## One registry, two front ends

Every operation is described once in `internal/ops`: its parameters, JSON Schema,
risk class and required scope. The CLI generates cobra commands from it, the MCP
server generates tools from it, and `schema` prints it.

The alternative — writing commands and tools separately — guarantees they drift.
A test asserts that every registered operation is well-formed and that risk
classes and scopes agree.

## A hand-written API client

The Zabbix API is a single `POST /api_jsonrpc.php` speaking JSON-RPC 2.0. The
existing Go clients target Zabbix 2.x to 5.x, authenticate with `user.login`
rather than the bearer token introduced in 6.4, and none is actively maintained.
`fabiang/go-zabbix` is alive but GPL-2.0 and oriented around typed session login.

Roughly three hundred lines gives full control over the things that matter here:
retries only for reads, credential redaction, a response size limit, tolerant
decoding of Zabbix's habit of returning every number as a string, and typed errors
that distinguish a rejected token from a missing permission.

A generic `Call(ctx, method, params, result)` is deliberate. Typing two hundred
API methods would produce a large surface that still needs the service layer above
it to be useful.

## The official MCP SDK

`github.com/modelcontextprotocol/go-sdk`, pinned. It reached v1, tracks the
specification, and offers both stdio and streamable HTTP.

`Server.AddTool` is used rather than the generic `AddTool`, because tools are
generated from the registry at runtime rather than from Go types known at compile
time. Input validation is then this program's responsibility — which it wanted
anyway, so that an unknown parameter is answered with the list of accepted ones.

One thing the SDK does not do by default is cross-origin protection: the field is
nil unless set. The HTTP transport wraps the handler in
`http.NewCrossOriginProtection` explicitly.

## Reads retry, writes do not

`CallIdempotent` retries transport failures and 5xx with jittered backoff.
`Call` never retries. After a connection fails mid-write there is no way to know
whether Zabbix applied the change, and repeating it could create a second
maintenance window or acknowledge an event twice.

## Aggregates prefer partial answers

`host investigate` fans out with a concurrency cap of four and collects failures
per leg. An engineer diagnosing an incident is better served by four facts out of
five plus a warning than by an error. The result carries `partial: true` so the
gap is visible rather than assumed away.

## Field projections rather than `output: extend`

A raw `host.get` on the reference installation returns about twelve thousand
characters, and `item.get` fifteen thousand — mostly configuration nothing reads.
Each domain type declares the fields diagnosis needs. Where the raw form is truly
required, `api call` provides it, with a warning that its output is neither
projected nor bounded.

## Where the Zabbix API surprises

Each of these was verified against a live 7.4.10 server, and each produces a
plausible wrong answer rather than an error:

- `problem.get` has no `selectHosts`. Hosts come from a batched
  `trigger.get(triggerids, selectHosts)`.
- `history.get` defaults to history type 3, so a float item returns nothing.
  Value types are read first and item IDs grouped by type.
- `item.lastvalue` and `item.lastclock` are accepted and return `"0"`.
- `host.available` does not exist; availability is per interface, plus
  `active_available` for the active agent.
- `event.acknowledge.action` is a bitmask whose documentation lists
  "6 - acknowledge event" while its own example computes 34 for acknowledge plus
  suppress. Acknowledge is bit 2. The public surface is an enum.
- `searchWildcardsEnabled: true` **disables** implicit substring matching. Leaving
  it permanently on turns every fragment search into an exact match — which is why
  a third of the calls against the tool this replaces returned nothing.
- `maintenance.create` wants `hosts: [{hostid}]`, not `hostids: []`.
- `apiinfo.version` is rejected if an Authorization header is present.

## Not built

No LLM client. "AI" here names the caller, not a dependency. A tool that calls a
model to decide what to do cannot be reasoned about by the model that called it.

No graphs, trends or report generation. The usage record that shaped this scope
contains not one request for them.

No per-tool write operations over MCP. `zabbix_write` and `zabbix_plan_create`
take an operation enum, so the tool surface stays fixed as operations are added.

## Build

The MCP SDK requires Go 1.25. Builds and tests run in a container, so a host with
an older toolchain still produces a correct binary:

```bash
make build test lint
```
