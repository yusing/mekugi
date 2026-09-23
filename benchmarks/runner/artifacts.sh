#!/usr/bin/env bash
# Benchmark artifacts phase. Sourcing only defines functions.

collect_router_metrics() {
	local arm=$1 destination=$2 capture=$3 directory
	local -a sessions=()
	for directory in "$run_dir/artifacts/$task_id/"*; do
		[[ -f $directory/arm ]] || continue
		[[ $(cat "$directory/arm") == "$arm" ]] && sessions+=("$directory")
	done
	((${#sessions[@]})) || return 1
	"${compose[@]}" run --rm --no-deps --no-tty \
		--volume "$run_dir:$run_dir" dependency-loader \
		mekugi-merge-captures "$destination" "$capture" "${sessions[@]}"
}

collect_artifacts() {
	[[ $started == true && $collected != true ]] || return 0
	local arm complete=true
	for arm in "${run_arms[@]}"; do
		collect_router_metrics "$arm" "$run_dir/${arm_metrics[$arm]}" "$run_dir/${arm_captures[$arm]}" || complete=false
	done
	collected=$complete
	[[ $complete == true ]]
}

collect_agent_issue_reports() {
	bash "$benchmark_root/collect-agent-issue-reports.sh" \
		"$issue_reports_directory" "$issue_reports"
}

import_control_baseline() {
	local baseline_result="$control_baseline_dir/artifacts/$task_id/${task_id}-control-r001/result.json"
	local destination="$run_dir/artifacts/$task_id/${task_id}-control-r001"
	local temporary="$destination/result.json.tmp"

	for path in "$control_baseline_dir/summary.md" "$control_baseline_dir/control-metrics.json" "$baseline_result"; do
		if [[ ! -s $path ]]; then
			printf "bench.sh: control baseline artifact is missing or empty: %s\n" "$path" >&2
			return 1
		fi
	done
	if ! jq -e --arg task "$task_id" --arg model "$model" --arg effort "$reasoning_effort" \
		--arg contract "$task_contract_sha256" --arg instructions "$control_instruction_sha" \
		'.task_id == $task and .arm == "control" and .model == $model and .reasoning_effort == $effort and .task_pass == true
		 and .task_contract_sha256 == $contract and .base_instructions.sha256 == $instructions' \
		"$baseline_result" >/dev/null; then
		printf "bench.sh: control baseline does not match the task content, instructions, model, reasoning, and passing-result contract: %s\n" "$baseline_result" >&2
		return 1
	fi
	mkdir -p "$destination"
	cp -a -- "$control_baseline_dir/artifacts/$task_id/${task_id}-control-r001/." "$destination/"
	cp -- "$control_baseline_dir/control-metrics.json" "$control_metrics"

	jq --arg previous "$control_baseline_dir/" --arg current "$run_dir/" \
		--arg summary "$control_baseline_dir/summary.md" \
		"walk(if type == \"string\" and startswith(\$previous) then \$current + ltrimstr(\$previous) else . end) | .imported_control_baseline = {summary: \$summary}" \
		"$baseline_result" >"$temporary"
	mv -f -- "$temporary" "$destination/result.json"
}

normalize_mekugi_artifact_permissions() {
	local config="$run_dir/mekugi-config"
	local runtime="$run_dir/mekugi-runtime"
	local reports_path=$issue_reports_directory
	local captures=$capture_directory
	local owner

	if docker info --format '{{json .SecurityOptions}}' | grep -Fq '"name=rootless"'; then
		owner=0:0
	else
		owner="$(id -u):$(id -g)"
	fi

	if ! docker run --rm \
		--mount type=bind,source="$config",target=/mekugi-config \
		--mount type=bind,source="$runtime",target=/mekugi-runtime \
		--mount type=bind,source="$reports_path",target=/agent-issue-reports \
		--mount type=bind,source="$captures",target=/captures \
		--mount type=bind,source="$run_dir/artifacts",target=/artifacts \
		--mount type=bind,source="$run_dir",target=/benchmark-run \
		"$benchmark_image" \
		sh -euc 'chown -R "$1" /mekugi-config /mekugi-runtime /agent-issue-reports /captures /artifacts; chmod -R u+rwX,go-rwx /mekugi-config /mekugi-runtime /agent-issue-reports /captures /artifacts
		for name in control-metrics.json mekugi-metrics.json mekugi-mentor-metrics.json; do
			path=/benchmark-run/$name
			if [ -f "$path" ]; then chown "$1" "$path"; chmod 600 "$path"; fi
		done' \
		sh "$owner"; then
		printf 'bench.sh: cannot normalize mekugi artifact permissions under %s\n' "$run_dir" >&2
		return 1
	fi
}

normalize_repository_permissions() {
	local repository=$1
	local owner

	if docker info --format '{{json .SecurityOptions}}' | grep -Fq '"name=rootless"'; then
		owner=0:0
	else
		owner="$(id -u):$(id -g)"
	fi

	if ! docker run --rm \
		--mount type=bind,source="$repository",target=/repository \
		"$benchmark_image" \
		sh -euc 'chown -R "$1" /repository; chmod -R u+rwX,go+rwX /repository' \
		sh "$owner"; then
		printf 'bench.sh: cannot normalize benchmark repository permissions at %s\n' "$repository" >&2
		return 1
	fi
}

normalize_codex_home_permissions() {
	local codex_home=$1
	local owner

	if docker info --format '{{json .SecurityOptions}}' | grep -Fq '"name=rootless"'; then
		owner=0:0
	else
		owner="$(id -u):$(id -g)"
	fi

	if ! docker run --rm \
		--mount type=bind,source="$codex_home",target=/benchmark-codex-home \
		"$benchmark_image" \
		sh -euc 'chown -R "$1" /benchmark-codex-home; chmod -R u+rwX,go-rwx /benchmark-codex-home' \
		sh "$owner"; then
		printf 'bench.sh: cannot normalize isolated Codex home permissions at %s\n' "$codex_home" >&2
		return 1
	fi
}

merge_results() {
	shopt -s nullglob
	result_files=("$run_dir"/artifacts/"$task_id"/*/result.json)
	if ((${#result_files[@]})); then
		jq -sc 'sort_by(.repetition, .order_in_block)[]' "${result_files[@]}" >"$results"
	fi
}

print_capture_summary() {
	local arm
	local metrics
	printf '\nCapture summary by benchmark arm:\n'
	printf 'arm\tlogical_requests\tprovider_attempts\tcompleted\tfailed\tcapture_errors\tincomplete_records\n'
	for arm in "${retained_arms[@]}"; do
		metrics="$run_dir/${arm_metrics[$arm]}"
		if [[ ! -s $metrics ]] || ! jq -e '.schema == "mekugi.capture.metrics.v6"' "$metrics" >/dev/null; then
			printf 'bench.sh: capture summary unavailable for %s\n' "$arm" >&2
			continue
		fi
		if ! jq -r --arg arm "$arm" '
			[
				$arm,
				.requests.logical,
				.requests.provider_attempts,
				.requests.completed,
				.requests.failed,
				.capture.capture_errors,
				.capture.incomplete_records
			] |
			@tsv
		' "$metrics"; then
			printf 'bench.sh: capture summary rendering failed for %s\n' "$arm" >&2
		fi
	done
}

rewrite_published_paths() {
	local previous_run_dir=$1
	local result
	local temporary

	shopt -s nullglob
	result_files=("$run_dir"/artifacts/"$task_id"/*/result.json)
	for result in "${result_files[@]}"; do
		temporary="$result.tmp"
		if ! jq --arg previous "$previous_run_dir/" --arg current "$run_dir/" '
			walk(
				if type == "string" and startswith($previous) then
					$current + ltrimstr($previous)
				else
					.
				end
			)
		' "$result" >"$temporary"; then
			rm -f -- "$temporary"
			return 1
		fi
		mv -f -- "$temporary" "$result"
	done
	merge_results
}

preserve_run() {
	local previous_run_dir=$run_dir

	local destination

	if [[ $previous_run_dir != "$results_root"/.staging-* ]]; then
		printf 'bench.sh: refusing to publish unexpected staging path: %s\n' "$previous_run_dir" >&2
		return 1
	fi
	destination="$results_root/$benchmark_commit-$task_id-${previous_run_dir##*/.staging-}"
	if [[ -e $destination || -L $destination ]]; then
		printf 'bench.sh: collision at unique result destination: %s\n' "$destination" >&2
		return 1
	fi
	if ! mv -T -- "$previous_run_dir" "$destination"; then
		printf 'bench.sh: cannot preserve benchmark run at %s\n' "$destination" >&2
		return 1
	fi

	run_dir=$destination
	results="$run_dir/results.jsonl"
	control_metrics="$run_dir/control-metrics.json"
	mekugi_metrics="$run_dir/mekugi-metrics.json"
	if [[ $benchmark_mode == mentor-handoff ]]; then
		control_metrics="$run_dir/mekugi-metrics.json"
		mekugi_metrics="$run_dir/mekugi-mentor-metrics.json"
	fi
	issue_reports_directory="$run_dir/agent-issue-reports"
	issue_reports="$run_dir/agent-issue-reports.jsonl"
	capture_directory="$run_dir/captures"
	benchmark_config="$run_dir/benchmark-config.json"
	instruction_dir="$run_dir/instructions"
	control_instruction="$instruction_dir/control.md"
	mekugi_instruction="$instruction_dir/mekugi.md"
	instruction_diff="$instruction_dir/control-to-mekugi-request.diff"
	mentor_parent_prompt="$instruction_dir/mentor-parent.md"
	mentor_child_prompt="$instruction_dir/mentor-child.md"
	mentor_child_role_config="$instruction_dir/mentor-child.toml"
	mentor_spawn_prompt="$instruction_dir/mentor-spawn-message.txt"
	export BENCH_RUN_DIR=$run_dir

	if ! rewrite_published_paths "$previous_run_dir"; then
		printf 'bench.sh: cannot rewrite paths after preserving run at %s\n' "$run_dir" >&2
		return 1
	fi
}

generate_summary() {
	if [[ ! -f $benchmark_root/report.sh ]]; then
		printf 'bench.sh: report generator is missing: %s\n' "$benchmark_root/report.sh" >&2
		return 1
	fi
	bash "$benchmark_root/report.sh" "$run_dir"
}

enforce_edit_loop_acceptance() {
	# Inspect stock command behavior without inferring edit success from model prose.
	if [[ $benchmark_mode == control-only ]]; then return; fi
	local -a mekugi_events=()
	if [[ $benchmark_mode == mentor-handoff ]]; then
		mapfile -t mekugi_events < <(
			find "$run_dir/artifacts" -type f \( -name codex.jsonl -o -name child-events.jsonl \) -print | sort
		)
	else
		mapfile -t mekugi_events < <(
			find "$run_dir/artifacts" -type f -path '*-mekugi-r*/codex.jsonl' -print | sort
		)
	fi
	bash "$benchmark_root/check-edit-loops.sh" "$benchmark_root" "${mekugi_events[@]}"
}

print_result_paths() {
	printf 'Results: %s\n' "$results"
	printf 'Artifacts: %s\n' "$run_dir/artifacts"
	if [[ $benchmark_mode == control-only ]]; then
		printf 'Control metrics: %s\n' "$control_metrics"
		return
	fi
	if [[ $benchmark_mode != mekugi-diagnostic ]]; then
		if [[ $benchmark_mode == mentor-handoff ]]; then
			printf 'Mekugi metrics: %s\n' "$control_metrics"
		else
			printf 'Control metrics: %s\n' "$control_metrics"
		fi
	fi
	if [[ $benchmark_mode == mentor-handoff ]]; then
		printf 'Mekugi + Mentor Handoff capture metrics: %s\n' "$mekugi_metrics"
	else
		printf 'Mekugi capture metrics: %s\n' "$mekugi_metrics"
	fi

}

cleanup() {
	local status=$?
	local pid
	local -a agent_containers=()
	if (($#)); then
		status=$1
	fi

	trap - EXIT
	trap '' INT TERM
	set +e
	for pid in "${worker_pids[@]}"; do
		kill -TERM "$pid" 2>/dev/null || true
	done
	for pid in "${worker_pids[@]}"; do
		wait "$pid" 2>/dev/null || true
	done
	worker_pids=()
	if ! merge_results; then
		printf 'bench.sh: cannot merge available benchmark results during cleanup\n' >&2
		status=1
	fi
	if [[ $compose_used == true ]]; then
		mapfile -t agent_containers < <(
			docker ps -aq \
				--filter "label=com.docker.compose.project=$COMPOSE_PROJECT_NAME" \
				--filter label=mekugi.benchmark.role=agent
		)
		if ((${#agent_containers[@]})); then
			docker rm --force "${agent_containers[@]}" >/dev/null 2>&1 || true
		fi
	fi
	if ! collect_artifacts; then
		printf 'bench.sh: cannot collect available benchmark artifacts during cleanup\n' >&2
		status=1
	fi
	if [[ $started == true ]] && ! normalize_mekugi_artifact_permissions; then
		if ((status == 0)); then
			status=1
		fi
	fi
	if [[ $started == true ]]; then
		if ! print_capture_summary; then
			printf 'bench.sh: capture summary failed\n' >&2
		fi
	fi
	if [[ $compose_used == true ]]; then

		"${compose[@]}" down --volumes --remove-orphans >/dev/null 2>&1 || true

	fi
	if ! collect_agent_issue_reports; then
		if ((status == 0)); then
			status=1
		fi
	fi
	if [[ -n $dependency_workspace && -d $dependency_workspace ]]; then
		rm -rf -- "$dependency_workspace"
		dependency_workspace=
	fi
	if [[ $dependency_cache == "$results_root"/.dependency-cache-* && -d $dependency_cache ]]; then
		if ! chmod -R u+w -- "$dependency_cache" || ! rm -rf -- "$dependency_cache"; then
			printf 'bench.sh: cannot remove dependency cache: %s\n' "$dependency_cache" >&2
			if ((status == 0)); then
				status=1
			fi
		fi
	else
		printf 'bench.sh: refusing to remove unexpected dependency cache path: %s\n' "$dependency_cache" >&2
		if ((status == 0)); then
			status=1
		fi
	fi
	if ! preserve_run; then
		if ((status == 0)); then
			status=1
		fi
	fi
	if [[ $prepare_only == false ]]; then
		if ! generate_summary; then
			if ((status == 0)); then
				status=1
			fi
		elif [[ $enforce_no_edit_loops == true ]] && ! enforce_edit_loop_acceptance; then
			if ((status == 0)); then
				status=1
			fi
		fi
	fi
	print_result_paths
	exit "$status"
}
