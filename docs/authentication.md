# Authentication and profiles

A profile is a named Zabbix installation. Credentials are stored separately from
configuration, so the configuration file can be read, diffed and committed
without leaking anything.

## Creating a profile

```bash
zabbix-ai-cli-mcp login --profile prod
```

The command asks for the URL and the API token, verifies the token against the
server, and only then writes anything. A typo is reported now rather than during
the next incident.

Non-interactively:

```bash
printf %s "$TOKEN" | zabbix-ai-cli-mcp login \
  --profile prod \
  --url https://zabbix.example.com \
  --token-stdin
```

There is deliberately no `--token` flag. Flag values appear in shell history and
in the process list, where anything on the machine can read them.

## Resolution order

The first of these that yields a token wins:

1. `--token-stdin`
2. `ZABBIX_AI_CLI_MCP_TOKEN`
3. `ZABBIX_AI_CLI_MCP_TOKEN_FILE` — a path; the file must not be readable by others
4. the profile's `token_file`
5. the OS keyring, if the profile selected it
6. the credentials file

The URL resolves independently: `ZABBIX_AI_CLI_MCP_URL` overrides the profile.
The profile itself comes from `--profile`, then `ZABBIX_AI_CLI_MCP_PROFILE`, then
`active_profile` in the configuration.

## The keyring never falls back silently

A profile stored with `--store keyring` reads from the keyring and nowhere else.
If the keyring is unavailable — which is normal in a container and over SSH,
where there is no D-Bus session — the command fails and names the alternatives.

Quietly writing the token to a file instead would downgrade the protection the
user asked for without telling them.

## Files

```
$XDG_CONFIG_HOME/zabbix-ai-cli-mcp/config.toml       0600, no secrets
$XDG_CONFIG_HOME/zabbix-ai-cli-mcp/credentials.toml  0600, tokens
$XDG_STATE_HOME/zabbix-ai-cli-mcp/plans/             0600, pending changes
$XDG_STATE_HOME/zabbix-ai-cli-mcp/audit.log          0600, applied changes
```

macOS and Windows use the platform's own configuration directory.
`ZABBIX_AI_CLI_MCP_CONFIG_DIR` and `ZABBIX_AI_CLI_MCP_STATE_DIR` override both.

Credential files are checked before being read: a symlink, a non-regular file, a
file owned by another user, or one readable by group or other is refused rather
than trusted.

## Configuration

```toml
active_profile = "prod"
allow_write = true

[profiles.prod]
url = "https://zabbix.example.com"
scopes = ["maintenance", "acknowledge"]
timeout_seconds = 30

[profiles.staging]
url = "https://stage.example.com"
ca_file = "/etc/ssl/private-ca.pem"
allow_write = false
```

## Writes

`allow_write` decides whether a change may be applied by the call that asked for
it. Absent means allowed. A profile's own value overrides the file-wide one, and
`ZABBIX_AI_CLI_MCP_ALLOW_WRITE` overrides both.

With it off, a write is described and stored as a plan, and a person applies it
with `zabbix-ai-cli-mcp approve`. What each profile currently allows is in
`profile show` and `auth status`. The reasoning is in
[the security model](security.md#the-write-setting).

## Scopes

Scopes bound what a profile may change. A profile that names some is held to
exactly those; a profile that names none may do whatever `allow_write` permits.
Reads never need a scope.

| Scope | Permits |
| --- | --- |
| `read` | nothing; reads need no scope |
| `maintenance` | maintenance windows |
| `acknowledge` | event updates |
| `configuration` | triggers, items, hosts and other configuration |

```bash
zabbix-ai-cli-mcp profile scopes prod --add maintenance
zabbix-ai-cli-mcp profile scopes prod --remove configuration
```

Removing a scope from a profile that named none writes the inherited set down
first, so that the removal narrows the profile instead of silently doing
nothing.

Scopes sit behind the permissions of the Zabbix token itself, which remains the
last real boundary. A read-only token cannot be widened by granting a scope.

## Containers and CI

Pass the token by environment or by file. A file is preferable where the
orchestrator supports secrets, because environment variables are visible to
anything that can read the process:

```bash
docker run --rm \
  -e ZABBIX_AI_CLI_MCP_URL=https://zabbix.example.com \
  -e ZABBIX_AI_CLI_MCP_TOKEN_FILE=/run/secrets/zabbix \
  -v /run/secrets/zabbix:/run/secrets/zabbix:ro \
  ghcr.io/stufently/zabbix-ai-cli-mcp mcp
```

## Checking

```bash
zabbix-ai-cli-mcp auth status
```

Reports the profile, the URL, where the token came from, the granted scopes and
whether the server accepts it. It describes the situation rather than failing, so
it is useful precisely when something is wrong.
