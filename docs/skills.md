# Skills

The repository ships five skills. They describe workflows, not logic: which
command to reach for, how to read the answer, and which mistakes produce a
confident wrong conclusion. No business logic is duplicated in them.

| Skill | Covers |
| --- | --- |
| `zabbix-status` | What is broken now |
| `zabbix-investigate` | Diagnosing one host |
| `zabbix-maintenance` | Silencing hosts, and finding out why alerts stopped |
| `zabbix-no-data` | Hosts and checks that went quiet |
| `zabbix-alert-analysis` | Why a notification did or did not arrive |

## Installing

```bash
zabbix-ai-cli-mcp skills list
zabbix-ai-cli-mcp skills install claude
zabbix-ai-cli-mcp skills install codex
```

Both runtimes read `skills/<name>/SKILL.md` with YAML front matter, so one set of
files serves both. By default they go to the user's directory
(`~/.claude/skills` or `~/.codex/skills`); `--project` installs into the current
directory instead.

An existing skill is left alone. `--force` overwrites, and says how many it
replaced.

## Why they exist

The commands are discoverable through `zabbix-ai-cli-mcp schema`, but discoverability
does not convey judgement. A skill is where the judgement lives: that suppressed
problems must be reported rather than filtered out; that an empty problem list is
not evidence of health until maintenance and no-data have been checked; that
chaining raw API calls to rebuild `host investigate` reproduces the API's traps
along with its data.

## Writing your own

Front matter needs a `name` and a `description`. The description is what the
runtime matches against when deciding whether the skill applies, so it should name
the words a person would actually use.

```markdown
---
name: zabbix-capacity
description: Assess Zabbix host capacity trends — disk filling up, memory pressure, growth rates. Use when asked whether a host will run out of something.
---

# Zabbix: capacity

1. `zabbix-ai-cli-mcp metrics history <host> "disk" --last 7d --json`
2. Read the `summary` block rather than every point.
3. ...
```

Keep them short and specific. A skill that restates the help text costs context
without adding judgement.
