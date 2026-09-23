#!/usr/bin/env bash
set -euo pipefail

benchmark_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
fixture=$(mktemp -d)
trap 'rm -rf -- "$fixture"' EXIT
capture="$fixture/docker-arguments"
codex_home="$fixture/codex-home"
auth="$fixture/auth.json"
mkdir "$codex_home"
: >"$auth"

cat >"$fixture/docker" <<'SH'
#!/bin/sh
printf '%s\n' "$@" >"$CAPTURE"
SH
chmod +x "$fixture/docker"

PATH="$fixture:$PATH" \
	CAPTURE="$capture" \
	MEKUGI_BENCH_COMPOSE_FILE="$fixture/compose.yaml" \
	BENCH_AGENT_SERVICE=control-agent \
	BENCH_RUN_DIR="$fixture" BENCH_ARTIFACT_DIR="$fixture/artifacts" MEKUGI_BENCH_MODE=passthrough \
	BENCH_CODEX_HOME="$codex_home" \
	CODEX_AUTH_PATH="$auth" \
	bash "$benchmark_root/codex-compose.sh" --version

grep -Fxq 'CODEX_HOME=/benchmark-codex-home' "$capture"
grep -Fxq "$codex_home:/benchmark-codex-home" "$capture"
grep -Fxq "$auth:/benchmark-codex-home/auth.json:ro" "$capture"

tail -n 3 "$capture" | diff -u - <(printf '%s\n' \
	control-agent mekugi-benchmark-session --version)
