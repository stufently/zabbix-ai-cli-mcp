---
name: zabbix-status
description: Answer "what is broken right now" from Zabbix. Use when asked about current alerts, active problems, whether a host is healthy, or for a monitoring overview. Triggers on mentions of Zabbix problems, alerts, outages, or a host name paired with a question about its state.
---

# Zabbix: current state

Answer questions about what is wrong now, using `zabbix-ai-cli-mcp`.

## Workflow

1. **Broad question** ("what's broken?", "any alerts?"):
   ```bash
   zabbix-ai-cli-mcp problems list --json
   ```
   Add `--severity high` to cut noise, `--limit N` to bound the answer.

2. **About one host**: pass it through. Matching is fuzzy, so a fragment works.
   ```bash
   zabbix-ai-cli-mcp problems list --host web01 --json
   zabbix-ai-cli-mcp host status web01 --json
   ```

3. **Nothing came back**: the host name may not be what you assumed.
   ```bash
   zabbix-ai-cli-mcp host list --search web --json
   ```

## Reading the result

- `suppressed: true` means the problem raises no alert. `suppressed_by` names the
  maintenance window responsible and when it ends. **Say so explicitly** — a
  suppressed problem looks identical to a healthy host from the outside, and an
  outage can sit inside one for weeks unnoticed.
- `acknowledged: false` means nobody has picked it up yet.
- `meta.truncated: true` means there is more; say so rather than implying the list
  is complete, and offer to raise `--limit`.
- `age` is how long the problem has been open. A problem hours old that nobody
  acknowledged is worth mentioning on its own.

## Do not

- Do not use `zabbix_api_call` for these questions. The high-level commands resolve
  host names and bound their output; a raw `problem.get` returns neither the host
  nor the suppression reason.
- Do not report a count without saying whether the result was truncated.
