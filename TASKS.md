# Work log

## COMPLETED — 2026-09-13 — the version stamp works again; released as 0.4.1

`internal/cli.Version` is a plain constant the linker can replace, and the module
version recorded by the toolchain is applied in `init` only when no stamp
arrived. What was wrong and why it stayed unnoticed is in CHANGELOG.md.

Verified: `make build` reports `v0.4.0-2-g28b1b52-dirty` where it used to report
`dev`; `make stamp-check` gets its sentinel back; putting the function
initialiser back turned both `stamp-check` and a unit test red, and flipping the
precedence in `resolveVersion` turned the unit test red.

## COMPLETED — 2026-09-13 — item writes are judged by type; released as 0.4.0

The blanket refusal of every `item.*` write is gone. `ClassifyCall` reads the
params it is handed and refuses only the types whose items execute something
(10, 11, 13, 14, 19, 20, 21), plus a write that does not state `type`, plus
`item.copy`, plus any batch containing one of those types. Details and reasoning
are in CHANGELOG.md and docs/security.md; the trigger was that an ordinary agent
check — the common case — could not be created at all.

Verified before the release: 32 subtests in `TestClassifyCallItemType`, three
mutations (dropping HTTP agent 19 from the executing set, allowing a missing
`type`, ignoring the array shape of a batch) each turned the suite red, and the
live stand refused a script and an SSH item while a trapper item was created and
deleted through the same binary.

Cutover on this host:

- `~/.claude.json` repinned from `ghcr.io/stufently/zabbix-ai-cli-mcp:0.3.0` to
  `:0.4.0`, with `~/.claude.json.bak-20260913-024617` alongside it for rollback.
- Codex runs `bin/zabbix-ai-cli-mcp` directly; rebuilt at the tag (v0.4.0).
- The released image was probed afterwards: a script item (type 20) and an
  `item.update` without `type` are refused, an agent item plans normally, and the
  probe plan was rejected — no scratch objects left in Zabbix.
- **Takes effect on the next Claude Code and Codex restart.**
- The drafts v0.2.0–v0.4.0 were published and v0.4.0 marked as the latest
  release; goreleaser no longer drafts (owner's call, 13.09.2026).

## COMPLETED — 2026-08-30 — allow_write replaces the mandatory approval gate

Writes no longer have to be planned and approved. One setting, `allow_write`,
decides; it defaults to on, and `false` restores the previous behaviour. Over
MCP that adds `zabbix_write`. Released as 0.3.0; what changed is in CHANGELOG.md.

Verified against the live Zabbix 7.4.10: a window created and deleted directly,
both audited, no approval step.

Cutover on this host:

- `~/.claude.json` repinned from `ghcr.io/stufently/zabbix-ai-cli-mcp:0.2.0` to
  `:0.3.0`, with `~/.claude.json.bak-20260830-025042` alongside it for rollback.
- Codex runs `bin/zabbix-ai-cli-mcp` directly; rebuilt from the tag.
- Skills reinstalled into `~/.claude/skills` and `~/.codex/skills`, because the
  maintenance skill told an agent it must never apply a change.
- **Takes effect on the next Claude Code and Codex restart.**

## COMPLETED — 2026-08-21 — zabbix-ai-cli-mcp v0.1 and cutover

Built the project, verified it against the live Zabbix 7.4.10, and replaced the
previous MCP server with it.

- Repository published at `github.com/stufently/zabbix-ai-cli-mcp`.
- Binary installed at `~/bin/zabbix-ai-cli-mcp`; profile `prod` carries the token
  transferred from the old container, with the `maintenance` and `acknowledge`
  scopes granted.
- Skills installed into `~/.claude/skills` and `~/.codex/skills`.
- The `zabbix` MCP entry in `~/.claude.json` now runs
  `/home/deploy/bin/zabbix-ai-cli-mcp mcp --profile prod` over stdio. The old
  container `zabbix-mcp-server` is stopped and removed; its image is kept.
- **Takes effect on the next Claude Code restart.**

Rollback, if it is ever needed: `docker compose up -d` in
`~/services/zabbix-mcp-server`, then restore `~/.claude.json` from the
`.bak-20260821-055539` copy alongside it.

What changed in the code is in CHANGELOG.md; the design and its reasoning are in
`docs/superpowers/specs/2026-08-21-zabbix-ai-cli-mcp-design.md`.

## COMPLETED — 2026-08-21 — review round two

Codex and an independent code review of the finished implementation; every
finding checked against the code before acting, since both reviewers produced
some that did not hold. What was real is fixed and listed under Security and
Fixed in CHANGELOG.md. The headline ones: `--http :8000` bound every interface
while counting as loopback, `api call` allowed `script.create` and
`action.create` (a longer road to the command execution `script.execute` is
refused for), a plan's risk and scope were trusted from the file rather than
re-derived, and the container could not start because its state directory did
not exist.

CI also failed on `golangci-lint`: the action resolved to a v1 build made with
an older Go than the module targets. Pinned to v2.12.2 and the `go` directive
lowered to 1.25.0, which is what the MCP SDK actually needs and what the README
promises.

## COMPLETED — 2026-08-21 — review of the Go 1.27 pass

Eight findings, six real. Fixed with tests: `plans list` aborting when one plan
was claimed under it, `zabbix_plan_status` stuck on "applying" behind a leftover
claim, `alert why` making two serial `user.get` calls per candidate, unsanitised
`read_error` reaching the terminal, numeric host names failing to resolve, and
`--store` validated only after `--token-stdin` had eaten the token. Also moved
the build cache out of the repository — it broke `make fmt-check` — and put the
`go` directive back to its real lower bound. Not acted on: the nil-`http.Client`
repair in `api.NewClient`, whose scenario `httpClient` cannot produce.

MCP binary on this host updated to `51f1643` via `make install`.

## COMPLETED — 2026-08-21 — public release v0.1.0 and discovery

The repository is public (gitleaks found nothing in its history first), tagged
v0.1.0, and published: release archives for six platforms, `ghcr.io/stufently/
zabbix-ai-cli-mcp` (anonymously pullable), and an entry in the official MCP registry
as `io.github.stufently/zabbix-ai-cli-mcp`.

Discovery work: repository description and 20 topics — `mcp-server` and
`model-context-protocol` are what Glama and PulseMCP crawl for; README rewritten
with per-client install snippets and a FAQ; CONTRIBUTING and a code of conduct.

The local MCP now runs the ghcr image instead of `~/bin`, pinned to `0.1.0`.
The profile is mounted read-only and the state directory read-write and shared,
so a plan created by an agent is still approved from the host with
`zabbix-ai-cli-mcp approve <id>`.

Rollback: restore `~/.claude.json` from `.bak-20260821-102243`, which points the
MCP back at `/home/deploy/bin/zabbix-ai-cli-mcp`.

Bumping the pinned image later means editing that one `args` entry in
`~/.claude.json` after the new tag's release finishes.

## COMPLETED — 2026-08-21 — v0.1.1 and the refusal-message fix

Live testing of the container MCP found a real bug: a method the risk registry
refuses outright, `usermacro.get`, was reported as "a write, use the planner".
The classifier returns an empty risk for a denied method and the write predicate
read "not a read" as "write", so the caller was sent to a gate that would refuse
it again for the wrong reason. Operations now answer a refusal question of their
own, asked before the read-or-plan decision; `api.call` answers it from the risk
registry. Released as v0.1.1 and the local MCP repinned to that tag.

Verified against the published image: `usermacro.get` now explains that macro
values hold passwords and that this output goes into a model's context.

Note for the next release: `.goreleaser.yaml` sets `release: draft: true`, so
the GitHub release is created as a draft and has to be published by hand
(`gh release edit <id> --tag vX.Y.Z --draft=false --latest`). The archives are
invisible until that step; the ghcr image and the MCP registry entry are not —
those go out at build time.

## TODO — discovery and promotion

Two assets do most of the work and both are already live: this repository
carrying the `mcp-server` and `model-context-protocol` topics, and the entry in
the official registry as `io.github.stufently/zabbix-ai-cli-mcp`. Everything below
either follows from those or reaches an audience the MCP directories do not.

Traffic figures are third-party estimates (SimilarWeb, via a May 2026 survey of
MCP directories). They rank the work; they are not promises.

### Verify what should list itself

Glama and PulseMCP crawl GitHub and the official registry, so no submission is
needed — only a check that it landed and that the card reads correctly.

- [ ] Glama (~105K/mo): card exists, description not truncated, tools detected
- [ ] PulseMCP (~277K/mo): same
- [ ] The registry entry still resolves and points at the current version:
      `curl -s 'https://registry.modelcontextprotocol.io/v0/servers?search=zabbix-ai-cli-mcp'`

### Submit by hand

Quick forms, roughly in order of reach. The `description` field is the most-read
text in every one of them — reuse the 93-character registry description rather
than writing something new each time.

- [ ] MCP Market (~1.4M/mo)
- [ ] mcpservers.org (~504K/mo)
- [ ] Smithery (~446K/mo)
- [ ] mcp.so (~238K/mo)
- [ ] Pull request to `punkpeye/awesome-mcp-servers` — other lists copy from it

### Reach the people who actually run Zabbix

The MCP directories are a market of AI tooling enthusiasts. The Zabbix audience
is somewhere else, and there this is not "another MCP server" but a fix for a
problem they already have. Two competitors are already in the registry
(`io.github.mhajder/zabbix-mcp`, `io.github.daedalus/mcp-zabbix`), so competing
only on the general directories is competing for incidental traffic.

- [ ] share.zabbix.com
- [ ] Zabbix community forum
- [ ] r/zabbix
- [ ] Russian-language sysadmin channels; a Habr article
- [ ] Answer existing "Zabbix + LLM" threads with a specific link, not a pitch

### Write the article that is already half-written

The README section on the six Zabbix 7.4 API traps — `problem.get` without
`selectHosts`, `history.get` silently empty for float items, `lastvalue` frozen
at `"0"`, `searchWildcardsEnabled` disabling substring matching — is original
material that does not exist anywhere else, and every one of those produces a
confident wrong answer rather than an error. It answers questions people search
for, which makes it the piece most likely to be cited by both people and models.

- [ ] Draft it as a standalone post, linking the tool rather than leading with it
- [ ] Cross-post: Habr, the Zabbix forum, dev.to

### Owner-only — cannot be done from the CLI

- [ ] Social preview image: Settings → Social preview. It decides whether a link
      shared on Twitter, Slack or Reddit gets clicked at all.
- [ ] Enable private vulnerability reporting: Settings → Security. SECURITY.md
      already points at the advisory form; the form does not exist until this is
      switched on. The API refused it with the current token scopes.

### Keep it fresh

- [ ] Bump the registry entry with each release — a new version resurfaces the
      card. This is automatic via GoReleaser; the task is to notice if it fails.
- [ ] Check pkg.go.dev has indexed the module (asynchronous; the proxy already
      serves v0.1.0)

### Bigger, optional

- [ ] A `.mcpb` bundle for Claude Desktop — one-click install removes the
      largest adoption barrier for non-technical users
- [ ] GitHub Pages from `docs/`: real indexable URLs, `llms.txt` and schema.org
      markup instead of a single README. Roughly a couple of hours' work and the
      largest remaining lever for ordinary search.

## IN_PROGRESS

Nothing.
