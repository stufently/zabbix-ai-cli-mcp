# Safe media type and lost LLD resource operations

> **For agentic workers:** Execute inline with test-driven development.

**Goal:** Implement the supplied task at `/var/tmp/claude-deploy-userbot/zabbix-mediatype-lld-task.md`.

**Architecture:** Register three domain operations in `internal/ops`, with planners in
`internal/service`. Restrict the two raw update methods by fields and validate them
again when applying stored plans. Reuse the existing approval, scope and audit path.

**Tech Stack:** Go, existing MCP SDK, fake JSON-RPC server, Docker.

## Constraints

- Use this clone and `feat/mediatype-lld-ops`; commit, do not push.
- Run tests/builds only in Docker as `1002:1002`; no live Zabbix or credentials.
- Keep version/tags; update Unreleased; imperative commit title at most 50 characters.
- No scheduler: media state changes are idempotent for external cron use.

## Review focus

- Mixed safe/unsafe batch requests fail before any write.
- Re-sealed plans cannot smuggle extra fields or downgrade deletion risk.
- Duplicate names, missing selectors, empty updates fail clearly.
- Nested template links count each host once; unrelated discovered items are excluded.
- Read failures stop planning rather than claiming an empty dependency/impact set.

## Tasks

### 1. Constrained raw writes and JSON input

Files: `internal/safety/registry.go`, `internal/opspec/opspec.go`,
`internal/ops/write.go`, corresponding tests.

- [x] Add classification tests for status-only media updates and four-field LLD updates,
  mixed batches, invalid IDs/statuses, and forbidden fields.
- [x] Watch tests fail in Docker; implement field-sensitive classification.
- [x] Accept JSON objects and JSON strings for `api.call.params` through a dedicated
  parameter type; publish both in JSON Schema, retain CLI string input.

### 2. Domain plans and execution

Files: new `internal/service/mediatypes.go`, `internal/service/lostresources.go`,
new `internal/ops/configuration.go`, `internal/service/rawapi.go`,
`internal/service/apply.go`, `internal/ops/execute.go`, tests.

- [x] Test MCP planning/write/audit for all three operations, raw updates and JSON input.
- [x] Test Script/Webhook state changes, idempotence, duplicate names, stale state.
- [x] Test host/template rule selection, nested template impact, lost item ownership,
  immediate deletion risk, forbidden mixed fields and re-sealed plans.
- [x] Watch tests fail; implement narrow projections, full dependency/impact reads,
  current/future descriptions and state preconditions.
- [x] Route raw limited updates through these planners, retaining raw operation audit.
- [x] Revalidate allowed payload and risk at apply time for domain and raw operations.

### 3. Discovery and delivery

Files: MCP operation descriptions generated from registry, `docs/mcp.md`, README,
CHANGELOG, CONTRIBUTING safety description.

- [x] Document parameters, safe raw exceptions, deletion effects, external cron example.
- [x] Run final `make all race UID=1002 GID=1002`; inspect diff and scope.
- [x] Commit task changes without pushing; report command and results.

## Review fixes

The independent read-only review identified three important cases. Regression
checks reproduce each before the fixes: all-media action dependencies, same-size
host/template replacement, and a re-sealed destructive plan dropping its confirm
target. The implementation includes all-media actions conservatively, compares
ID sets for topology, and derives constrained-operation confirmation targets.
Additional boundary tests cover numeric policy modes and noncanonical raw numeric
strings so the plan's description agrees with classification.

Deletion modes 0/2 and deletion-duration-only edits are conservatively destructive.
No background scheduler was added, as explicitly permitted by the task.

Validation: `make all race UID=1002 GID=1002` and `git diff --check` passed.
