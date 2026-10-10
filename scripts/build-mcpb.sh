#!/usr/bin/env bash
# Build the six Desktop Extension bundles (.mcpb).
# Local and CI both enter here. VERSION is required (CI passes the tag
# without the leading v; the acceptance check passes 9.9.9). The same value
# is written into manifest.json and stamped into the binary with ldflags.
# Nothing is installed on the host: Go and the mcpb CLI run in Docker.
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

if [ -z "${VERSION:-}" ]; then
  echo "VERSION is required" >&2
  exit 1
fi
if ! printf '%s\n' "$VERSION" | grep -Eq '^[0-9A-Za-z][0-9A-Za-z._+-]*$'; then
  echo "VERSION=${VERSION} is not a safe version token" >&2
  exit 1
fi

GO_IMAGE=golang:1.27.2
NODE_IMAGE=node:24.21.0-slim
MCPB_CLI=@anthropic-ai/mcpb@2.1.2
ROOT=$PWD
OUT="$ROOT/dist-mcpb"
CACHE="${HOME}/.cache/zabbix-ai-cli-mcp"
NPM_CACHE="${XDG_CACHE_HOME:-$HOME/.cache}/promo-mcpb-npm"
UID_GID="$(id -u):$(id -g)"
LDFLAGS="-s -w -X github.com/stufently/zabbix-ai-cli-mcp/internal/cli.Version=${VERSION}"
TARGETS=(linux-amd64 linux-arm64 darwin-amd64 darwin-arm64 windows-amd64 windows-arm64)

rm -rf "$OUT"
mkdir -p "$OUT" "$CACHE" "$NPM_CACHE"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
export UID_GID GO_IMAGE WORK="$work" VERSION ROOT

# One container cross-compiles every goreleaser target. CGO is off, so the
# binaries are static and the linux/amd64 one runs in the image used later.
docker run --rm -u "$UID_GID" \
  -v "$ROOT":/src -v "$CACHE":/cache -v "$work":/out \
  -e GOCACHE=/cache/build -e GOMODCACHE=/cache/mod -e GOFLAGS=-buildvcs=false \
  -e CGO_ENABLED=0 -e "LDFLAGS=${LDFLAGS}" \
  -w /src "$GO_IMAGE" \
  bash -c '
    set -euo pipefail
    for pair in linux-amd64 linux-arm64 darwin-amd64 darwin-arm64 windows-amd64 windows-arm64; do
      goos=${pair%-*}
      goarch=${pair##*-}
      name=zabbix-ai-cli-mcp
      if [ "$goos" = windows ]; then name=zabbix-ai-cli-mcp.exe; fi
      mkdir -p "/out/${pair}/server"
      GOOS=$goos GOARCH=$goarch go build -trimpath -ldflags "$LDFLAGS" \
        -o "/out/${pair}/server/${name}" ./cmd/zabbix-ai-cli-mcp
    done
  '

chmod 755 "$work/linux-amd64/server/zabbix-ai-cli-mcp"

# tools/list from the binary just built, with writes on, so the manifest
# declares the same fifteen tools the server will list. Dummy credentials:
# listing tools does not call Zabbix.
python3 - "$work/tools.json" <<'PY'
import json, os, select, subprocess, sys, time

out_path = sys.argv[1]
cmd = [
    "docker", "run", "--rm", "-i",
    "-u", os.environ["UID_GID"],
    "-e", "HOME=/tmp",
    "-e", "ZABBIX_AI_CLI_MCP_URL=http://127.0.0.1:9",
    "-e", "ZABBIX_AI_CLI_MCP_TOKEN=dummy",
    "-e", "ZABBIX_AI_CLI_MCP_ALLOW_WRITE=true",
    "-e", "ZABBIX_AI_CLI_MCP_STATE_DIR=/tmp/zabbix-state",
    "-v", os.environ["WORK"] + ":/out:ro",
    os.environ["GO_IMAGE"],
    "/out/linux-amd64/server/zabbix-ai-cli-mcp", "mcp",
]
proc = subprocess.Popen(cmd, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
buf = b""
next_id = 1

def send(obj):
    proc.stdin.write((json.dumps(obj) + "\n").encode())
    proc.stdin.flush()

def request(method, params, timeout):
    global next_id, buf
    rid = next_id
    next_id += 1
    send({"jsonrpc": "2.0", "id": rid, "method": method, "params": params})
    deadline = time.time() + timeout
    while time.time() < deadline:
        while b"\n" in buf:
            line, buf = buf.split(b"\n", 1)
            line = line.strip()
            if not line:
                continue
            try:
                msg = json.loads(line)
            except ValueError:
                continue
            if isinstance(msg, dict) and msg.get("id") == rid and "method" not in msg:
                if "error" in msg:
                    raise SystemExit("%s -> %s" % (method, msg["error"]))
                return msg.get("result", {})
        ready, _, _ = select.select([proc.stdout], [], [], 1.0)
        if ready:
            chunk = proc.stdout.read1(65536) if hasattr(proc.stdout, "read1") else proc.stdout.readline()
            if not chunk:
                err = proc.stderr.read().decode("utf-8", "replace")[-2000:]
                raise SystemExit("server closed stdout during %s; stderr: %s" % (method, err))
            buf += chunk
    raise SystemExit("timeout waiting for %s" % method)

try:
    request("initialize", {
        "protocolVersion": "2025-06-18",
        "capabilities": {},
        "clientInfo": {"name": "build-mcpb", "version": "1"},
    }, 180)
    send({"jsonrpc": "2.0", "method": "notifications/initialized"})
    tools, cursor = [], None
    for _ in range(100):
        params = {"cursor": cursor} if cursor else {}
        res = request("tools/list", params, 60)
        tools.extend(res.get("tools", []))
        cursor = res.get("nextCursor")
        if not cursor:
            break
finally:
    try:
        proc.stdin.close()
    except OSError:
        pass
    try:
        proc.wait(timeout=10)
    except subprocess.TimeoutExpired:
        proc.kill()
        proc.wait()

names = [t.get("name") for t in tools]
if len(tools) != 15 or "zabbix_write" not in names:
    raise SystemExit("expected 15 tools including zabbix_write, got %s" % names)
with open(out_path, "w", encoding="utf-8") as f:
    json.dump(tools, f)
print("listed %d tools" % len(tools))
PY

python3 - "$ROOT/mcpb/manifest.json" "$work/tools.json" "$work" <<'PY'
import json, os, sys

manifest_path, tools_path, work = sys.argv[1:]
version = os.environ["VERSION"]
with open(manifest_path, encoding="utf-8") as f:
    base = json.load(f)
with open(tools_path, encoding="utf-8") as f:
    live = json.load(f)
base.pop("version", None)
base["version"] = version
base["tools"] = [{"name": t["name"], "description": t.get("description") or ""} for t in live]
declared = {t["name"] for t in base["tools"]}
if len(declared) != 15:
    raise SystemExit("manifest tools collapsed to %s" % sorted(declared))

platforms = {"linux": "linux", "darwin": "darwin", "windows": "win32"}
for name in os.listdir(work):
    stage = os.path.join(work, name)
    if not os.path.isdir(stage) or "-" not in name:
        continue
    goos = name.split("-", 1)[0]
    entry = "server/zabbix-ai-cli-mcp.exe" if goos == "windows" else "server/zabbix-ai-cli-mcp"
    doc = json.loads(json.dumps(base))
    doc["compatibility"] = {"platforms": [platforms[goos]]}
    doc["server"]["entry_point"] = entry
    doc["server"]["mcp_config"]["command"] = "${__dirname}/" + entry
    doc["server"]["mcp_config"]["args"] = ["mcp"]
    path = os.path.join(stage, "manifest.json")
    with open(path, "w", encoding="utf-8") as f:
        json.dump(doc, f, indent=2)
        f.write("\n")
    print("manifest %s -> %s" % (name, entry))
PY

# Pack each staged directory. The directory holds only the manifest and the
# binary, so tests, git metadata and env files never enter the archive.
cat > "$work/pack.sh" <<EOF
#!/bin/bash
set -euo pipefail
for pair in ${TARGETS[*]}; do
  npx -y ${MCPB_CLI} pack "/out/\${pair}" "/dist/zabbix-ai-cli-mcp-${VERSION}-\${pair}.mcpb"
done
EOF
chmod 755 "$work/pack.sh"

docker run --rm -u "$UID_GID" \
  -e HOME=/tmp -e npm_config_cache=/npm \
  -v "$NPM_CACHE":/npm -v "$work":/out -v "$OUT":/dist \
  "$NODE_IMAGE" bash /out/pack.sh

found=$(find "$OUT" -maxdepth 1 -name '*.mcpb' | wc -l)
if [ "$found" -ne "${#TARGETS[@]}" ]; then
  echo "expected ${#TARGETS[@]} bundles, found $found" >&2
  ls -la "$OUT" >&2
  exit 1
fi
echo "built $found bundles in dist-mcpb/ for $VERSION"
