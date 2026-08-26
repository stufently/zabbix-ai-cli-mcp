---
name: zabbix-investigate
description: Diagnose one host in Zabbix — why it is alerting, flapping, or silent. Use when asked to investigate, troubleshoot, or explain what is happening with a specific server or host.
---

# Zabbix: investigate a host

Collect the facts about one host, then reason over them. The tool gathers; you interpret.

## Workflow

1. **One call gathers the context.** Do not assemble it by hand.
   ```bash
   zabbix-ai-cli-mcp host investigate web01 --json
   ```
   This returns host state, agent availability, active problems, recent events,
   silent and unsupported items, and any maintenance window covering the host.

2. **Follow the strongest signal**, in this order:
   - `status.agent_available: "unavailable"` → Zabbix cannot reach the host at all.
     The interface `error` field usually says why. Everything else is downstream of this.
   - `status.in_maintenance: true` → alerts are suppressed. Check whether the window
     should still be open before treating silence as health.
   - `no_data_items` non-empty → the host answers but specific checks stopped
     reporting. Compare their keys: one subsystem or all of them?
   - `unsupported_items` non-empty → the check itself is failing, and `error` says how.
     This is a monitoring fault, not necessarily a host fault.
   - `active_problems` → read `severity` and `age` together; an old average-severity
     problem is often more telling than a fresh warning.

3. **Values over time**, when a problem is about a threshold:
   ```bash
   zabbix-ai-cli-mcp metrics history web01 "cpu util" --last 24h --json
   ```
   The `summary` block gives min, average and max without reading every point.

4. **Recent history**: `recent_events` covers the last 24 hours. Several
   problem-and-resolve pairs for the same trigger mean flapping, not a single fault.

## Reading the result

- `meta.partial: true` means a sub-query failed and the rest is still valid. Say
  which part is missing rather than presenting the snapshot as complete.
- No-data detection samples the first 40 scheduled items; a warning says so when the
  cap applied. Items that arrive by trapper or are derived from others are excluded
  on purpose — silence from one of those is not evidence of a fault.

## Do not

- Do not chain `zabbix_api_call` to rebuild this. `problem.get` cannot return hosts,
  and `item.lastvalue` has been a constant zero for years; both mistakes produce a
  confident, wrong answer.
- Do not conclude "the host is fine" from an empty problem list alone. Check
  maintenance and no-data first.
