---
name: zabbix-no-data
description: Find Zabbix hosts and items that stopped reporting — unreachable agents, silent checks, unsupported items. Use when asked why a host went quiet, which agents are down, or whether monitoring itself is broken.
---

# Zabbix: silence and no-data

Silence is ambiguous: a host that reports nothing looks the same as a host with
nothing wrong. These commands tell the two apart.

## Workflow

1. **Across the installation**:
   ```bash
   zabbix-ai-cli-mcp unreachable --json
   ```
   Lists monitored hosts with an unavailable interface, with the error text Zabbix
   recorded and whether the host is in maintenance.

2. **For one host**:
   ```bash
   zabbix-ai-cli-mcp host investigate web01 --json
   ```
   Read `no_data_items` and `unsupported_items` together with `status.agent_available`.

3. **For specific checks**:
   ```bash
   zabbix-ai-cli-mcp metrics latest web01 --search disk --json
   ```
   Each value carries `no_data`, `stale` and `age`.

## Distinguishing the causes

- **Agent unreachable** — the interface is unavailable and carries an error. Nothing
  on that host is reporting; investigate connectivity, not individual checks.
- **Item unsupported** — the check runs and fails. `error` says why: a missing
  binary, a permission problem, a changed key. This is a monitoring fault.
- **Item silent but supported** — the check stopped producing values without
  erroring. Often a template or interval change.
- **In maintenance without data collection** — the gap is expected. Check
  `maintenance list` before calling it a fault.

## Reading the result

`stale` means the newest value is older than three collection intervals. It is only
computed for items that collect on a schedule: trapper and dependent items arrive
when something else sends or derives them, so silence from one is not a fault.
Items with a flexible or scheduling interval fall back to a conservative threshold
and are not flagged on thin evidence.

## Do not

- Do not read `lastclock` or `lastvalue` from a raw `item.get`. Both have returned a
  constant zero for several releases, which makes every item look silent.
