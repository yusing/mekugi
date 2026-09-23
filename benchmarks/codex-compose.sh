#!/usr/bin/env bash
set -euo pipefail

: "${MEKUGI_BENCH_COMPOSE_FILE:?MEKUGI_BENCH_COMPOSE_FILE must be set}"
: "${BENCH_AGENT_SERVICE:?BENCH_AGENT_SERVICE must be set}"

codex_home_options=()
if [[ -n ${BENCH_CODEX_HOME:-} ]]; then
	: "${CODEX_AUTH_PATH:?CODEX_AUTH_PATH must be set when BENCH_CODEX_HOME is set}"
	codex_home_options=(
		--env CODEX_HOME=/benchmark-codex-home
		--volume "$BENCH_CODEX_HOME:/benchmark-codex-home"
		--volume "$CODEX_AUTH_PATH:/benchmark-codex-home/auth.json:ro"
	)
fi

case $BENCH_AGENT_SERVICE in
	control-agent|mekugi-agent) ;;
	*)
		printf 'codex-compose.sh: unsupported agent service: %s\n' "$BENCH_AGENT_SERVICE" >&2
		exit 1
		;;
esac

: "${BENCH_ARTIFACT_DIR:?BENCH_ARTIFACT_DIR must be set}"
: "${BENCH_RUN_DIR:?BENCH_RUN_DIR must be set}"
session_runtime="$BENCH_RUN_DIR/mekugi-runtime/${BENCH_ARTIFACT_DIR##*/}"

exec docker compose -f "$MEKUGI_BENCH_COMPOSE_FILE" run \
	--interactive=false \
	--no-tty \
	--rm \
	--no-deps \
	--env "MEKUGI_RUNTIME_DIR=$session_runtime" \
	--volume "$session_runtime:$session_runtime" \
	--env "BENCH_ARTIFACT_DIR=$BENCH_ARTIFACT_DIR" \
	--env "MEKUGI_BENCH_MODE=${MEKUGI_BENCH_MODE:?}" \
	--env "MEKUGI_BENCH_MAIN_MENTOR=${MEKUGI_BENCH_MAIN_MENTOR:-false}" \
	--env "MEKUGI_BENCH_MENTOR=${MEKUGI_BENCH_MENTOR:-false}" \
	--volume "$BENCH_ARTIFACT_DIR:$BENCH_ARTIFACT_DIR" \
	--env GIT_CONFIG_COUNT=1 \
	--env GIT_CONFIG_KEY_0=safe.directory \
	--env "GIT_CONFIG_VALUE_0=$PWD" \
	"${codex_home_options[@]}" \
	--volume "$PWD:$PWD" \
	--workdir "$PWD" \
	"$BENCH_AGENT_SERVICE" \
	mekugi-benchmark-session "$@"
