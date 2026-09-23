#!/usr/bin/env bash
set -euo pipefail

benchmark_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
model=${MODEL:-gpt-5.6-luna}
reasoning_effort=${REASONING_EFFORT:-medium}

case $model in
gpt-5.6-luna|gpt-5.6-terra|gpt-6-luna|gpt-6-sol) ;;
*)
	printf 'run-commentary-coverage.sh: MODEL must be gpt-5.6-luna, gpt-5.6-terra, gpt-6-luna, or gpt-6-sol, got %s\n' "$model" >&2
	exit 2
	;;
esac

status=0
run_mode() {
	local mode=$1
	local report_issues=$2
	printf 'commentary coverage: starting %s\n' "$mode"
	if ! TASK_ID=commentary-coverage \
		MODEL="$model" \
		REASONING_EFFORT="$reasoning_effort" \
		REPETITIONS=1 \
		BENCHMARK_MODE="$mode" \
		BENCHMARK_REPORT_ISSUES="$report_issues" \
		BENCHMARK_ENFORCE_NO_EDIT_LOOPS=false \
		bash "$benchmark_root/bench.sh"; then
		status=1
	fi
}

run_mode mekugi-diagnostic true
run_mode mentor-handoff false
exit "$status"
