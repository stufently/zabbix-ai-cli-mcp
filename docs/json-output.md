# JSON output and exit codes

## Choosing the format

`--output json`, `--output table`, or `--json` as shorthand. The default is
`auto`: a table when stdout is a terminal, JSON otherwise. A command in a pipe or
a subprocess therefore produces machine-readable output without being asked, and
a person at a terminal gets something readable.

## Success

```json
{
  "ok": true,
  "data": {},
  "warnings": [],
  "meta": {
    "returned": 50,
    "total": 381,
    "limit": 50,
    "truncated": true,
    "truncated_reason": "row_limit",
    "next_cursor": "",
    "partial": false,
    "zabbix_version": "7.4.10",
    "profile": "prod",
    "elapsed_ms": 84
  }
}
```

`warnings` is always an array, never absent or null.

### meta

| Field | Meaning |
| --- | --- |
| `returned` | rows in `data` |
| `total` | rows available, when known without a second query |
| `limit` | the bound that was applied |
| `truncated` | there is more; the answer is not complete |
| `truncated_reason` | `row_limit` or `byte_limit` |
| `partial` | an aggregate where some sub-query failed and the rest is valid |
| `zabbix_version` | the connected server |

Truncation is detected by asking Zabbix for `limit + 1` rows, so a bounded answer
costs no extra query. Serialised JSON is never cut mid-structure.

**Treat `truncated: true` as part of the answer.** Reporting a count from a
truncated list as if it were the total is the most common way a correct query
produces a wrong conclusion.

## Failure

```json
{
  "ok": false,
  "error": {
    "code": "HOST_NOT_FOUND",
    "message": "host 'server01' was not found",
    "retryable": false,
    "suggestion": "run 'zabbix-ai-cli-mcp host list --search server01' to find the exact name; matching is fuzzy"
  }
}
```

`retryable` says whether repeating the same call could succeed: a timeout is
retryable, a rejected token is not. Errors never contain credentials.

### Codes

| Code | Meaning |
| --- | --- |
| `INVALID_ARGUMENTS` | a parameter is missing, unknown or malformed |
| `AUTHENTICATION_FAILED` | Zabbix rejected the token |
| `PERMISSION_DENIED` | the token lacks the permission |
| `SCOPE_NOT_GRANTED` | the profile may not plan this operation |
| `OPERATION_DENIED` | the risk registry refuses this method |
| `NOT_FOUND`, `HOST_NOT_FOUND` | no such resource |
| `AMBIGUOUS_MATCH` | a fuzzy pattern matched several resources |
| `ZABBIX_API_ERROR` | the server reported an error |
| `CONNECTION_FAILED`, `TIMEOUT` | the server could not be reached in time |
| `APPROVAL_REQUIRED` | a change needs `--confirm` or a terminal approval |
| `PLAN_EXPIRED`, `PLAN_PRECONDITION_FAILED`, `PLAN_NOT_FOUND` | the plan cannot be applied |
| `UNSUPPORTED_ZABBIX_VERSION` | the server is too old for this operation |
| `NO_PROFILE` | nothing is configured |
| `INTERNAL_ERROR` | a defect in this program |

## Exit codes

| Code | Meaning |
| --- | --- |
| 0 | success |
| 1 | generic failure |
| 2 | invalid arguments |
| 3 | authentication failure |
| 4 | connection or API failure |
| 5 | not found |
| 6 | permission denied |
| 7 | approval required |
| 8 | unsupported Zabbix version |

A write that only produced a plan exits **0**: it did what was asked, and
`data.status` is `planned`. Exit 7 means an attempt was made to apply something
and the authorisation was missing or wrong. A non-zero exit therefore never
accompanies `ok: true`.

## Self-description

```bash
zabbix-ai-cli-mcp schema                  # every operation
zabbix-ai-cli-mcp schema host.investigate # one operation, with its JSON Schema
zabbix-ai-cli-mcp schema api-methods      # the raw methods the escape hatch accepts
```

The output is generated from the same operation registry the CLI commands and the
MCP tools are built from, so it cannot drift from the implementation.

## Untrusted values

Host names, problem texts and item values come from Zabbix and may have been
written by anyone who can register a host. They are returned as data: control
characters are stripped and each field is length-bounded. No decision this program
makes depends on their content.
