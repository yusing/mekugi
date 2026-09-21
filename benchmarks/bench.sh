#!/usr/bin/env bash
# shellcheck source-path=SCRIPTDIR
# The runner can be sourced for local contract tests without starting a run.
benchmark_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=runner/configuration.sh
source "$benchmark_root/runner/configuration.sh"
# shellcheck source=runner/task.sh
source "$benchmark_root/runner/task.sh"
# shellcheck source=runner/preparation.sh
source "$benchmark_root/runner/preparation.sh"
# shellcheck source=runner/execution.sh
source "$benchmark_root/runner/execution.sh"
# shellcheck source=runner/artifacts.sh
source "$benchmark_root/runner/artifacts.sh"

benchmark_main() {
	configure_benchmark
	initialize_run
	trap cleanup EXIT
	trap 'exit 130' INT
	trap 'exit 143' TERM

	for executable in awk chmod cp curl date diff docker git go grep id jq mv sha256sum sort tar timeout wc; do
		if ! command -v "$executable" >/dev/null; then
			printf 'bench.sh: %s is required\n' "$executable" >&2
			exit 1
		fi
	done
	if [[ $prepare_only == false && ! -f $CODEX_AUTH_PATH ]]; then
		printf 'bench.sh: Codex auth file not found: %s\n' "$CODEX_AUTH_PATH" >&2
		exit 1
	fi
	if ! load_task_manifest; then
		exit 1
	fi
	if [[ $benchmark_mode == ctp-only && -z $expected_final_response ]]; then
		printf 'bench.sh: ctp-only mode requires task expected_final_response for decoded-output parity\n' >&2
		exit 2
	fi
	if [[ $source_is_public == true ]]; then
		git init --bare --quiet "$source_repo"
		git --git-dir="$source_repo" fetch --quiet --depth=1 "$source_repository" "$base_commit"
		git --git-dir="$source_repo" fetch --quiet --depth=1 "$source_repository" "$oracle_commit"
	elif [[ $source_kind == git && ! -d $source_repo/.git ]]; then
		printf 'bench.sh: source clone not found: %s\n' "$source_repo" >&2
		exit 1
	fi


	mkdir -p "$run_dir/work" "$run_dir/mekugi-config" "$capture_directory" \
		"$run_dir/mekugi-runtime/control" "$run_dir/mekugi-runtime/mekugi" "$instruction_dir"
	: >"$results"

	run_phase image-build build_benchmark_image
	run_phase instructions prepare_instructions
	prepare_mentor_prompts
	configure_issue_reporting
	run_phase dependencies prepare_dependency_cache
	printf 'Control base instructions: %s\n' "$control_instruction"
	printf 'Mekugi base instructions: %s\n' "$mekugi_instruction"
	if [[ $benchmark_mode == ctp-only ]]; then
		printf 'Native and CTP/2-active receive the same pre-router instructions; the router selects protocol guidance.\n'
	fi
	if [[ $benchmark_mode == mentor-handoff ]]; then
		printf 'Both arms use the same static %s/%s parent prompt and %s/%s child role and prompt; only the mekugi-mentor router enables the child handoff.\n' \
			"$mentor_parent_model" "$mentor_parent_reasoning_effort" "$model" "$reasoning_effort"
	fi
	printf 'Base instruction override source: %s\n' "${instruction_source:-none (tool guidance is projected separately)}"
	printf 'Base instruction diff: %s\n' "$instruction_diff"

	run_phase base-qualification validate_revision base "$base_commit" fail
	if [[ $source_kind == git ]]; then
		run_phase oracle-qualification validate_revision oracle "$oracle_commit" pass
	fi

	if [[ $prepare_only == true ]]; then
		printf 'Preparation and qualification passed for %s; model benchmark was not started.\n' "$task_id"
		exit 0
	fi

	run_phase session-preparation prepare_sessions

	if [[ $dependency_workspace == "$run_dir"/dependency-source-* ]]; then
		rm -rf -- "$dependency_workspace"
		dependency_workspace=
	else
		printf 'bench.sh: refusing to remove unexpected dependency workspace: %s\n' "$dependency_workspace" >&2
		exit 1
	fi

	benchmark_status=0
	for ((repetition = 1; repetition <= repetitions; repetition += 1)); do
		run_block "$repetition" &
		worker_pids+=("$!")
	done
	for pid in "${worker_pids[@]}"; do
		if ! wait "$pid"; then
			benchmark_status=1
		fi
	done
	worker_pids=()

	merge_results
	expected_results=$((repetitions * ${#run_arms[@]} + ${#imported_arms[@]}))
	if ((${#result_files[@]} != expected_results)); then
		printf 'bench.sh: found %d result records, want %d\n' \
			"${#result_files[@]}" "$expected_results" >&2
		benchmark_status=1
	fi

	collect_artifacts


	shopt -s nullglob
	diffs=("$run_dir"/artifacts/*/*/changes.patch)
	for diff in "${diffs[@]}"; do
		printf '\nAgent diff: %s\n' "$diff"
	done

	trap - EXIT
	cleanup "$benchmark_status"
}

if [[ ${BASH_SOURCE[0]} == "$0" ]]; then
	set -euo pipefail
	benchmark_main "$@"
fi
