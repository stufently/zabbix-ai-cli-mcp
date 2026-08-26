# Command line

Every command accepts `--json`, `--profile`, `--limit` where it returns a list,
and `--debug` to log API calls on stderr with credentials redacted.

## Reading

```bash
zabbix-ai-cli-mcp problems list
zabbix-ai-cli-mcp problems list --severity high --since 24h --limit 20
zabbix-ai-cli-mcp problems list --host web01 --unacknowledged
zabbix-ai-cli-mcp problems get 757474

zabbix-ai-cli-mcp host list --search web
zabbix-ai-cli-mcp host list "ms*" --monitored
zabbix-ai-cli-mcp host get web01
zabbix-ai-cli-mcp host status web01
zabbix-ai-cli-mcp host investigate web01

zabbix-ai-cli-mcp metrics latest web01 --search cpu
zabbix-ai-cli-mcp metrics history web01 "cpu util" --last 24h

zabbix-ai-cli-mcp triggers list --host web01 --problems
zabbix-ai-cli-mcp maintenance list
zabbix-ai-cli-mcp unreachable

zabbix-ai-cli-mcp alert why 757474
zabbix-ai-cli-mcp resolve "$(pbpaste)"
```

Host matching is a case-insensitive substring over both the technical and the
visible name, with exact matches ranked first. `*` is honoured when present.
A pattern matching several hosts is an error listing them, because silently
picking one risks acting on the wrong machine.

## Changing

Write commands describe the change and stop:

```bash
zabbix-ai-cli-mcp maintenance create "ms*" --for 2h
```

```
PLAN pl_cc89d2e87d15

Create maintenance "ms* (2h0m)" for 2h0m, ...

Affects:
  host ms1.8qw.ru
  ...

Nothing has changed yet.
To apply it: zabbix-ai-cli-mcp approve pl_cc89d2e87d15
```

Add `--apply` to make the change in the same command. Destructive commands also
require `--confirm` naming the target back exactly:

```bash
zabbix-ai-cli-mcp maintenance create "ms*" --for 2h --apply
zabbix-ai-cli-mcp maintenance extend 42 --by 24h --apply
zabbix-ai-cli-mcp maintenance expire 42 --apply
zabbix-ai-cli-mcp maintenance delete 42 --apply --confirm "weekend window"

zabbix-ai-cli-mcp events acknowledge 757474 --operations ack,message --message "investigating"
zabbix-ai-cli-mcp events acknowledge 757474 --operations close --apply --confirm 757474

zabbix-ai-cli-mcp triggers disable 35246 --apply --confirm 35246
zabbix-ai-cli-mcp triggers enable 35246 --apply
```

Acknowledge operations are named, never numbered: `ack`, `message`, `close`,
`severity`, `unack`, `suppress`, `unsuppress`. The underlying bitmask is easy to
get wrong, and getting it wrong closes a problem that was meant to be commented on.

## Approving

A change requested over MCP arrives as a stored plan:

```bash
zabbix-ai-cli-mcp plans list
zabbix-ai-cli-mcp plans show pl_cc89d2e87d15
zabbix-ai-cli-mcp approve pl_cc89d2e87d15
zabbix-ai-cli-mcp reject pl_cc89d2e87d15
```

`approve` prints the plan and asks before doing anything. Plans expire after
fifteen minutes. `--yes` skips the prompt and is required when stdin is not a
terminal, so a non-interactive approval is always deliberate.

## The escape hatch

```bash
zabbix-ai-cli-mcp api call host.get --params '{"output":["hostid","host"],"limit":5}'
zabbix-ai-cli-mcp api call hostinterface.update --params '{"interfaceid":"402","ip":"10.0.0.9"}' --apply
```

Read methods run immediately. Write methods produce a plan like any other change.
A method absent from the risk registry is refused; `zabbix-ai-cli-mcp schema
api-methods` lists the accepted ones with their risk class.

The escape hatch exists because the alternative is worse. The first task the
high-level commands do not cover otherwise gets done with curl and a copied
token, leaving no record at all.

## Profiles

```bash
zabbix-ai-cli-mcp profile list
zabbix-ai-cli-mcp profile show prod
zabbix-ai-cli-mcp profile use prod
zabbix-ai-cli-mcp profile scopes prod --add maintenance
zabbix-ai-cli-mcp profile delete staging

zabbix-ai-cli-mcp --profile staging problems list
```

## Self-description

```bash
zabbix-ai-cli-mcp schema
zabbix-ai-cli-mcp schema host.investigate
zabbix-ai-cli-mcp schema api-methods
```
