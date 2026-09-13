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

check() {
	binary="$1"
	kind="$2"
	reported="$("$binary" --version)"
	case "$reported" in
	*"$sentinel"*)
		echo "the version stamp reaches the binary ($kind): $reported"
		;;
	*)
		echo "the -X version stamp is dead ($kind): built with $sentinel, the binary reports \"$reported\"" >&2
		exit 1
		;;
	esac
}

check "$out/zabbix-ai-cli-mcp" "no VCS information"

# The same build with the VCS record present: there the stamp and the module
# version both exist, and the stamp has to win. Without this half the check
# still passes when the two sources are consulted in the wrong order, because
# the build above leaves the module version empty. Outside a repository (a
# source tarball) the toolchain cannot produce the record at all, so that case
# is skipped rather than failed.
if go build -trimpath -buildvcs=true \
	-ldflags "-X $pkg.Version=$sentinel" \
	-o "$out/with-vcs" ./cmd/zabbix-ai-cli-mcp 2>"$out/vcs-build.err"; then
	check "$out/with-vcs" "with VCS information"
else
	echo "skipped the build with VCS information: $(cat "$out/vcs-build.err")"
fi
