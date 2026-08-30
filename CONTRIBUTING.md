# Contributing

Thanks for looking. This project is small on purpose, and the fastest way to get
a change merged is to understand two constraints before writing code.

## The two rules that shape everything

**One operation registry.** The CLI, the MCP tools and `zabbix-ai-cli-mcp schema`
are all generated from `internal/opspec` + `internal/ops`. A new capability is a
registry entry, not three parallel implementations. If you find yourself editing
a cobra command and an MCP tool to say the same thing, stop — that is the bug.

**Applying a change is a configuration decision, not a parameter.** Whether an
MCP client may write is `allow_write`, read from the config file, the profile or
the environment. Never add an `apply`, `confirm` or `force` parameter to an MCP
tool: a confirmation an agent can send is a confirmation prompt injection can
send. A test in `internal/mcp` fails the build if one appears, and that test is
not the obstacle — it is the design. `zabbix_write` is the one tool that
applies, it takes no such parameter, and it re-checks the setting at execution
rather than trusting what was registered at startup.

See [docs/architecture.md](docs/architecture.md) for why.

## Building and testing

Everything runs in containers, so you do not need Go on your machine:

```bash
make build      # Linux binary in ./bin
make test       # go test ./...
make race       # go test -race ./...
make lint       # golangci-lint, pinned version
make fmt        # gofmt -w .
make docker     # container image
```

`make all` runs the format check, vet, tests and build — the same gate CI runs.
Please get it green locally before opening a pull request; CI runs the identical
commands and it is faster to find out on your own machine.

## Working against a real Zabbix

Unit tests use `internal/zbxtest`, a fake JSON-RPC server. It deliberately
mirrors Zabbix's quirks — string-rendered numbers, `filter` semantics — because a
fake that is friendlier than the server hides bugs rather than catching them. If
you discover a new quirk, teach the fake about it in the same change.

Behaviour that depends on a real server should be stated in the commit message
with the Zabbix version you saw it on.

## Adding a Zabbix API method to the escape hatch

`internal/safety/registry.go` is an explicit table, not a heuristic. An
unclassified method is refused rather than guessed at, and objects whose
configuration is code — scripts, actions, items, media types — are refused for
writes whatever scope a profile holds. If you need one of those, say what for in
the pull request; the answer may be a first-class operation instead.

## Style

Match the surrounding code. Comments explain *why*, especially where the code
works around something surprising in the Zabbix API — those comments are the
main defence against someone "simplifying" a workaround back into a bug.

## Reporting a vulnerability

Please do not open a public issue. See [SECURITY.md](SECURITY.md).
