---
name: zabbix-maintenance
description: Manage Zabbix maintenance windows — silence hosts for planned work, check what is currently suppressed, extend or end a window. Use when asked to silence, mute, or schedule downtime for hosts, or to find out why alerts stopped.
---

# Zabbix: maintenance windows

## Check first

```bash
zabbix-ai-cli-mcp maintenance list --json
```

Expired windows are shown deliberately: "that one already ended, remove it" is a
routine follow-up and cannot be done if the window is invisible. `ends_in` tells you
how long an active window has left.

## Opening a window

Host names may be patterns, so a fleet can be silenced the way it is described:

```bash
zabbix-ai-cli-mcp maintenance create "ms*,massivegrid*" --for 7d
zabbix-ai-cli-mcp maintenance create db1,db2 --for 2h --description "index rebuild"
```

Durations are written the way people say them: `30m`, `2h`, `7d`, `2w`.

**Nothing happens yet.** The command prints a plan showing exactly which hosts
matched, and stops. A pattern that matches nothing is an error, not a quietly
smaller window — check the host count in the plan before approving.

**You do not apply it.** `--apply` exists for a person working in their own
terminal; reaching for it from an agent session — over MCP or through a shell —
defeats the point of the plan. Relay the `approve_command` from the plan
verbatim and let the operator run it.

## Data collection

By default Zabbix keeps collecting during the window and only suppresses alerts.
`--no-data-collection` stops collection entirely — which also means a real outage
starting inside the window leaves no trace. Prefer the default unless asked.

## Ending a window

```bash
zabbix-ai-cli-mcp maintenance expire 42     # ends now, keeps the record
zabbix-ai-cli-mcp maintenance delete 42     # removes it entirely
```

Prefer `expire`. `delete` is destructive and needs the window named back exactly.

## Do not

- Do not open an indefinite window "to stop the noise". Give it the shortest
  duration that covers the work; a forgotten window is how an outage hides.
- Do not report a window as created before it has been approved. Until then,
  nothing has changed.
