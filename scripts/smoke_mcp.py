#!/usr/bin/env python3
"""Start an MCP server over stdio and check it lists its tools.

Usage: smoke_mcp.py COMMAND [ARG...]

CI runs it against the container image started with no environment and no
network, which is how registries such as Glama build and inspect it. The server
must answer initialize and tools/list, and a tool call must fail with the
"no Zabbix profile" error rather than reach anywhere.
"""

import json
import queue
import subprocess
import sys
import threading

TIMEOUT = 30


def main() -> int:
    if len(sys.argv) < 2:
        sys.exit("usage: smoke_mcp.py COMMAND [ARG...]")
    proc = subprocess.Popen(
        sys.argv[1:],
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        stderr=sys.stderr,
        text=True,
    )
    lines: "queue.Queue[str | None]" = queue.Queue()

    def pump() -> None:
        for line in proc.stdout:
            lines.put(line)
        lines.put(None)

    threading.Thread(target=pump, daemon=True).start()

    def send(msg: dict) -> None:
        proc.stdin.write(json.dumps(msg) + "\n")
        proc.stdin.flush()

    def reply(want_id: int) -> dict:
        while True:
            try:
                line = lines.get(timeout=TIMEOUT)
            except queue.Empty:
                sys.exit(f"no reply to request {want_id} within {TIMEOUT}s")
            if line is None:
                sys.exit(f"server exited before replying to request {want_id}")
            try:
                msg = json.loads(line)
            except json.JSONDecodeError:
                sys.exit(f"not JSON-RPC on stdout: {line[:200]!r}")
            if msg.get("id") == want_id:
                if "error" in msg:
                    sys.exit(f"request {want_id} failed: {msg['error']}")
                return msg["result"]

    try:
        send({"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {
            "protocolVersion": "2025-06-18", "capabilities": {},
            "clientInfo": {"name": "smoke", "version": "0"}}})
        init = reply(1)
        if "tools" not in init.get("capabilities", {}):
            sys.exit(f"initialize advertises no tools: {init}")
        send({"jsonrpc": "2.0", "method": "notifications/initialized"})

        send({"jsonrpc": "2.0", "id": 2, "method": "tools/list"})
        tools = [t["name"] for t in reply(2).get("tools", [])]
        if "zabbix_problems" not in tools:
            sys.exit(f"tools/list lacks zabbix_problems: {tools}")

        send({"jsonrpc": "2.0", "id": 3, "method": "tools/call",
              "params": {"name": "zabbix_problems", "arguments": {}}})
        call = reply(3)
        text = json.dumps(call)
        if not call.get("isError") or "NO_PROFILE" not in text:
            sys.exit(f"an unconfigured tool call should fail with NO_PROFILE: {text[:400]}")
    finally:
        proc.stdin.close()
        try:
            proc.wait(timeout=TIMEOUT)
        except subprocess.TimeoutExpired:
            proc.kill()

    print(f"{init['serverInfo']['name']} {init['serverInfo'].get('version', '')}: "
          f"{len(tools)} tools; an unconfigured call fails with NO_PROFILE")
    return 0


if __name__ == "__main__":
    sys.exit(main())
