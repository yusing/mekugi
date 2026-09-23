#!/usr/bin/env bash
# Benchmark task phase. Sourcing only defines functions.

is_allowed_path() {
	local candidate=$1
	local allowed
	for allowed in "${allowed_paths[@]}"; do
		if [[ $allowed == . && -n $candidate && $candidate != . &&
			$candidate != /* && $candidate != .. && $candidate != ../* &&
			$candidate != */../* && $candidate != */.. ]]; then
			return 0
		fi
		if [[ $candidate == "$allowed" ]]; then
			return 0
		fi
	done
	return 1
}

compute_task_contract() {
	local fingerprint
	fingerprint=$(cd -- "$task" && sha256sum -- "${task_manifest##*/}" "$prompt_file" "${hidden_sources[@]}" | sha256sum) || return 1
	task_contract_sha256=${fingerprint%% *}
}

verify_task_contract() {
	local current
	current=$(compute_task_contract && printf '%s' "$task_contract_sha256") || return 1
	if [[ $current != "$task_contract_sha256" ]]; then
		printf 'bench.sh: task content changed during this run; refusing mismatched execution or grading\n' >&2
		return 1
	fi
}

load_task_manifest() {
	local repository
	local manifest_relative
	if [[ $task_id == etcd-range-stream ]]; then
		task="$benchmark_root/tasks/$task_id"
		task_manifest="$task/task.json"
	elif manifest_relative=$(jq -er --arg id "$task_id" \
		'.tasks[] | select(.id == $id) | .manifest' "$suite_manifest"); then
		task_manifest="$benchmark_root/$manifest_relative"
		task=$(dirname "$task_manifest")
	else
		printf 'bench.sh: unknown TASK_ID: %s\n' "$task_id" >&2
		return 1
	fi
	if ! jq -e --arg id "$task_id" '.id == $id' "$task_manifest" >/dev/null; then
		printf 'bench.sh: task manifest id mismatch: %s\n' "$task_manifest" >&2
		return 1
	fi
	source_kind=$(jq -r '.source.kind // "git"' "$task_manifest")
	prompt_file=$(jq -er '.prompt_file' "$task_manifest")
	case $source_kind in
	git)
		repository=$(jq -er '.source.repository' "$task_manifest")
		source_repository=$repository
		case $repository in
		https://github.com/*.git)
			source_is_public=true
			source_repo="$run_dir/source.git"
			;;
		*)
			source_repo=$(cd "$task/$repository" && pwd)
			;;
		esac
		base_commit=$(jq -er '.source.base_commit' "$task_manifest")
		oracle_commit=$(jq -er '.source.oracle_commit' "$task_manifest")
		;;
	empty_fixture)
		base_commit=$(jq -er '.source.base_revision' "$task_manifest")
		;;
	*)
		printf 'bench.sh: unsupported source kind for %s: %s\n' "$task_id" "$source_kind" >&2
		return 1
		;;
	esac
	if [[ ! -f "$task/$prompt_file" ]]; then
		printf 'bench.sh: prompt file not found: %s\n' "$task/$prompt_file" >&2
		return 1
	fi
	mapfile -t allowed_paths < <(jq -er '.allowed_path_prefixes[]' "$task_manifest")
	mapfile -t hidden_sources < <(jq -er '.hidden_files[].source' "$task_manifest")
	mapfile -t hidden_paths < <(jq -er '.hidden_files[].destination' "$task_manifest")
	compute_task_contract || return 1
	mapfile -t grader_command < <(jq -er '.graders[0].command[]' "$task_manifest")
	grader_name=$(jq -er '.graders[0].name' "$task_manifest")
	baseline_output_contains=$(jq -er '.graders[0].baseline_output_contains' "$task_manifest")

	agent_timeout=$(jq -er '.agent_timeout_seconds' "$task_manifest")
	grader_timeout=$(jq -er '.graders[0].timeout_seconds' "$task_manifest")
	dependency_kind=$(jq -er '.runtime.dependency_kind // "go"' "$task_manifest")
	preload_go_qualification_grader=$(jq -er \
		'.runtime.preload_go_qualification_grader // false' "$task_manifest")
	if ! jq -e '(.expected_final_response // "") | type == "string"' "$task_manifest" >/dev/null; then
		printf 'bench.sh: expected_final_response must be a string for %s\n' "$task_id" >&2
		return 1
	fi
	expected_final_response=$(jq -r '.expected_final_response // ""' "$task_manifest")
	if ! python3 "$benchmark_root/check_commentary_coverage.py" validate \
		"$task_manifest" "$benchmark_mode"; then
		return 1
	fi
	if jq -e '.commentary_coverage != null' "$task_manifest" >/dev/null; then
		commentary_coverage_enabled=true
	fi
	case $dependency_kind in
	go|node|none) ;;
	*)
		printf 'bench.sh: unsupported dependency kind for %s: %s\n' "$task_id" "$dependency_kind" >&2
		return 1
		;;
	esac
	case $preload_go_qualification_grader in
	true)
		if [[ $source_kind != git || $dependency_kind != go ||
			${grader_command[0]-} != go || ${grader_command[1]-} != test ]]; then
			printf 'bench.sh: preload_go_qualification_grader requires Git source and a Go test grader for %s\n' \
				"$task_id" >&2
			return 1
		fi
		;;
	false) ;;
	*)
		printf 'bench.sh: runtime.preload_go_qualification_grader must be true or false for %s\n' \
			"$task_id" >&2
		return 1
		;;
	esac
	if ((${#hidden_sources[@]} == 0 || ${#hidden_sources[@]} != ${#hidden_paths[@]})); then
		printf 'bench.sh: hidden file mappings are empty or unbalanced\n' >&2
		return 1
	fi
	if ((${#allowed_paths[@]} == 0 || ${#grader_command[@]} == 0)); then
		printf 'bench.sh: task manifest has no allowed paths or grader command\n' >&2
		return 1
	fi
}
