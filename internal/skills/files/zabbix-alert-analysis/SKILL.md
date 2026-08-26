---
name: zabbix-alert-analysis
description: Explain why a Zabbix alert did or did not arrive. Use when asked why there was no notification, why an alert was missed, why a problem was not reported, or to trace notification delivery for an event.
---

# Zabbix: why was there no alert

A notification can be dropped at five different points, and most of them record no
error anywhere. Do not reconstruct the chain by hand.

## Workflow

1. **Get the event identifier.** If you have the notification text — pasted from
   chat, email, or a ticket — hand it over whole:
   ```bash
   zabbix-ai-cli-mcp resolve "Problem started at 08:17 ... Host: web01 ... Original problem ID: 757474" --json
   ```
   It extracts the event, host and trigger. Without this step an instruction like
   "why didn't this one alert?" cannot be acted on, because the identifiers exist
   only inside the message.

2. **Explain the delivery**:
   ```bash
   zabbix-ai-cli-mcp alert why 757474 --json
   ```

3. **Read `findings` first.** They are ordered facts, not conclusions. The supporting
   detail sits in `suppressed_by`, `delivery_attempts`, `actions`, `media_types` and
   `recipients`.

## What each link means

- **Suppressed** — the event fell inside a maintenance window. Nothing else matters;
  `suppressed_by` names the window and its end.
- **Delivery attempts exist and failed** — Zabbix tried. `error` carries the
  transport's own message: a rejected token, a refused connection, a bad address.
- **No delivery attempts at all** — nothing was ever queued, so the cause is
  configuration. Work down: is any trigger action enabled; is the media type
  enabled; does any recipient have media for it.
- **Recipients listed but `severity_accepted: false`** — the user's media filters
  this severity out. This drops notifications silently and is the single easiest
  cause to miss.
- **`media_enabled: false`** — that contact is switched off.

## Reading the result

`meta.partial: true` means one leg of the chain could not be read; say which.
A finding of "no obstacle was found in the configuration" means the chain is intact
and the cause lies outside Zabbix — at the transport or the recipient's client.

## Do not

- Do not answer from `problem.get` or `alert.get` alone. Delivery attempts only exist
  once an action fired; their absence is the interesting case, and it needs the
  action, media type and recipient checks to interpret.
