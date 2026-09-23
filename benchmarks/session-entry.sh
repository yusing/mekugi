#!/usr/bin/env bash
set -euo pipefail
: "${BENCH_ARTIFACT_DIR:?}"
: "${MEKUGI_BENCH_MODE:?}"
: "${MEKUGI_RUNTIME_DIR:?}"
export XDG_STATE_HOME="$MEKUGI_RUNTIME_DIR/state"
mkdir -p "$XDG_STATE_HOME"
# Each container owns one wrapper, one private listener, and one Codex invocation.
exec mekugi --mode "$MEKUGI_BENCH_MODE" \
 "--main-mentor-handoff=${MEKUGI_BENCH_MAIN_MENTOR:-false}" \
 "--mentor-handoff=${MEKUGI_BENCH_MENTOR:-false}" \
 --capture-output "$BENCH_ARTIFACT_DIR/capture.jsonl" \
 --metrics-output "$BENCH_ARTIFACT_DIR/metrics.json" codex --disable apps "$@"
