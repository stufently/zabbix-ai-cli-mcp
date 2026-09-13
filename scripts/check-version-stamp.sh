#!/bin/sh
# Fails if "-X …internal/cli.Version=…" no longer reaches the built binary.
#
# The defect this guards against is silent. A variable whose initialiser calls a
# function is overwritten at package init, so the linker's value disappears —
# and the binary still reports something plausible, because the Go toolchain
# stamps the module version from the VCS tag on its own. Only a build that has a
# stamp and no VCS information can tell the two sources apart, which is why
# -buildvcs=false is part of the check rather than an optimisation.
#
# Expects "go" on PATH: "make stamp-check" runs it inside the build container,
# CI runs it directly.
set -eu

sentinel="v0.0.0-stamp-check"
pkg="github.com/stufently/zabbix-ai-cli-mcp/internal/cli"
out="$(mktemp -d)"
trap 'rm -rf "$out"' EXIT

go build -trimpath -buildvcs=false \
	-ldflags "-X $pkg.Version=$sentinel" \
	-o "$out/zabbix-ai-cli-mcp" ./cmd/zabbix-ai-cli-mcp

reported="$("$out/zabbix-ai-cli-mcp" --version)"
case "$reported" in
*"$sentinel"*)
	echo "the version stamp reaches the binary: $reported"
	;;
*)
	echo "the -X version stamp is dead: built with $sentinel, the binary reports \"$reported\"" >&2
	exit 1
	;;
esac
