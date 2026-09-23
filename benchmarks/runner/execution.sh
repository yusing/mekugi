#!/usr/bin/env bash
# Benchmark execution phase. Sourcing only defines functions.

cancel_pair() {
	local status=$1

	trap - INT TERM
	pair_canceled=true
	pair_cancel_status=$status
	if [[ -n $active_agent_pid ]]; then
		kill -TERM -- "-$active_agent_pid" 2>/dev/null ||
			kill -TERM "$active_agent_pid" 2>/dev/null ||
			true
	fi
}

run_agent() {
	verify_task_contract || return 1
	local arm=$1
	local repetition=$2
	local order=$3
	local run_id
	run_id="$task_id-$arm-r$(printf '%03d' "$repetition")"
	local repository
	local artifact_dir="$run_dir/artifacts/$task_id/$run_id"
	local agent_service=${arm_services[$arm]}
	local codex_stdout="$artifact_dir/codex.jsonl"
	local codex_stderr="$artifact_dir/codex.stderr"
	local codex_home=
	local child_events=
	local child_proof=
	local started_at
	local started_ms
	local duration_ms
	local exit_code
	local timed_out=false
	local canceled=false
	local task_pass=true
	local grader_started_ms
	local grader_duration_ms
	local grader_exit
	local diff_path="$artifact_dir/changes.patch"
	local result_path="$artifact_dir/result.json"
	local agent_json
	local unauthorized_json='[]'
	local changed_json
	local grader_json
	local result_json
	local instruction_name=${arm_instructions[$arm]}
	local instruction_path="$instruction_dir/$instruction_name"
	local instruction_sha=$control_instruction_sha
	local instruction_diff_for_arm=
	local router_mode=${arm_modes[$arm]}
	local attempts_per_repetition=$((${#run_arms[@]} + ${#imported_arms[@]}))
	local executor_process_creation_errors=0
	local expected_response_required=false
	local expected_response_passed=true
	local commentary_coverage_json=null
	local commentary_coverage_path="$artifact_dir/commentary-coverage.json"
	local commentary_coverage_status=0
	local agent_prompt
	local root_model=$model
	local root_reasoning_effort=$reasoning_effort
	local -a codex_feature_args=()

	local path
	local input_tokens
	local cached_tokens
	local output_tokens
	local reasoning_tokens
	local -a changed=()
	local -a unauthorized=()

	if [[ $instruction_name == mekugi.md ]]; then
		instruction_sha=$mekugi_instruction_sha
		instruction_diff_for_arm=$instruction_diff
	fi
	printf 'run %s: repetition %d %s (%d/%d)\n' \
		"$task_id" "$repetition" "$arm" "$order" "$attempts_per_repetition"
	workspace=$(mktemp -d "$run_dir/work/$run_id-XXXXXX") || return 1
	trusted=$(mktemp -d /tmp/mekugi-capture-XXXXXX) || return 1
	repository="$workspace/repo"
	mkdir -p "$artifact_dir" || return 1
	printf '%s\n' "$arm" >"$artifact_dir/arm" || return 1
	if [[ $benchmark_mode == mentor-handoff ]]; then
		codex_home="$artifact_dir/codex-home"
		child_events="$artifact_dir/child-events.jsonl"
		child_proof="$artifact_dir/child-proof.json"
		mkdir -m 0770 "$codex_home" || return 1
	fi
	snapshot "$base_commit" "$repository" || return 1
	link_task_dependencies "$repository" || return 1
	python3 "$benchmark_root/capture_tree.py" baseline "$repository" "$trusted" || return 1

	agent_prompt=$(cat "$task/$prompt_file") || return 1
	if [[ $benchmark_mode == mentor-handoff ]]; then
		root_model=$mentor_parent_model
		root_reasoning_effort=$mentor_parent_reasoning_effort
		codex_feature_args=(
			-c 'features.multi_agent_v2=true'
			-c "agents.$mentor_child_role.description=\"Fixed benchmark implementation role\""
			-c "agents.$mentor_child_role.config_file=\"/bench-instructions/${mentor_child_role_config##*/}\""
		)
		agent_prompt=$(cat "$mentor_parent_prompt") || return 1
	fi
	started_at=$(date --utc --iso-8601=ns)
	started_ms=$(date +%s%3N)
	(
		cd "$repository" || exit 1
		export BENCH_AGENT_SERVICE=$agent_service
		export BENCH_ARTIFACT_DIR=$artifact_dir
		export MEKUGI_BENCH_MODE=$router_mode
		export MEKUGI_BENCH_MAIN_MENTOR=$main_mentor
		export MEKUGI_BENCH_MENTOR=${arm_mentor[$arm]}
		if [[ -n $codex_home ]]; then
			export BENCH_CODEX_HOME=$codex_home
		fi
		exec timeout --signal=TERM --kill-after=10s "${agent_timeout}s" \
			"$benchmark_root/codex-compose.sh" \
			-c "model_instructions_file=\"/bench-instructions/$instruction_name\"" \
			-c 'supports_websockets=true' \
			--model "$root_model" \
			-c "model_reasoning_effort=\"$root_reasoning_effort\"" \
			"${codex_feature_args[@]}" \
			exec \
			--dangerously-bypass-approvals-and-sandbox \
			--json \
			--color never \
			-C "$repository" \
			"$agent_prompt"
	) >"$codex_stdout" 2>"$codex_stderr" &
	active_agent_pid=$!
	if wait "$active_agent_pid"; then exit_code=0; else exit_code=$?; fi
	if [[ $pair_canceled == true ]]; then
		wait "$active_agent_pid" 2>/dev/null || true
	fi
	active_agent_pid=
	if [[ $benchmark_mode == mentor-handoff ]]; then
		if ! normalize_codex_home_permissions "$codex_home"; then
			task_pass=false
		elif ! python3 "$benchmark_root/collect_child_events.py" \
			"$codex_stdout" "$codex_home" "$child_events" "$child_proof" \
			"$mentor_child_role" "$mentor_child_prompt" "$model" "$reasoning_effort"; then
			task_pass=false
		fi
	fi
	canceled=$pair_canceled
	[[ $canceled != true ]] || return "$pair_cancel_status"
	duration_ms=$(($(date +%s%3N) - started_ms))
	executor_process_creation_errors=$(grep -Fc 'Failed to create unified exec process:' "$codex_stderr" || true)
	executor_process_creation_errors=${executor_process_creation_errors:-0}
	if [[ $exit_code -eq 124 || $exit_code -eq 137 ]]; then
		timed_out=true
	fi
	if [[ $exit_code -ne 0 || $canceled == true ]]; then
		task_pass=false
	fi
	normalize_repository_permissions "$repository" || return 1
	python3 "$benchmark_root/capture_tree.py" capture "$repository" "$trusted" \
		"$artifact_dir/changed-paths.json" "$diff_path" || return 1
	changed_json=$(cat "$artifact_dir/changed-paths.json") || return 1
	jq -j '.[] + "\u0000"' <<<"$changed_json" >"$trusted/changed-paths" || return 1
	mapfile -d '' changed <"$trusted/changed-paths" || return 1
	# Grade only the captured bytes, never the executor's mutable Git workspace.
	repository="$trusted/candidate"
	for path in "${changed[@]}"; do
		if ! is_allowed_path "$path"; then
			unauthorized+=("$path")
			task_pass=false
		fi
	done
	if ((${#unauthorized[@]})); then
		unauthorized_json=$(printf '%s\0' "${unauthorized[@]}" | jq -Rs 'split("\u0000")[:-1]') || return 1
	fi

	if ! inject_hidden_tests "$repository" >"$artifact_dir/grader-$task_id.stdout" 2>"$artifact_dir/grader-$task_id.stderr"; then
		grader_exit=125
		grader_duration_ms=0
	else
		grader_started_ms=$(date +%s%3N)
		if grade "$repository" "$artifact_dir/grader-$task_id.stdout" "$artifact_dir/grader-$task_id.stderr"; then
			grader_exit=0
		else
			grader_exit=$?
		fi
		grader_duration_ms=$(($(date +%s%3N) - grader_started_ms))
	fi

	if [[ $grader_exit -ne 0 ]]; then
		task_pass=false
	fi

	if ! agent_json=$(jq -s \
		--argjson exit_code "$exit_code" \
		--argjson canceled "$canceled" \
		--argjson timed_out "$timed_out" \
		--argjson duration_ms "$duration_ms" \
		--argjson executor_process_creation_errors "$executor_process_creation_errors" \
		--arg stdout_path "$codex_stdout" \
		--arg stderr_path "$codex_stderr" '
		{
			exit_code: $exit_code,
			timed_out: $timed_out,
			canceled: $canceled,
			duration_ms: $duration_ms,
			error: (
				[.[] |
					select(.type == "turn.failed" or .type == "error") |
					(.error.message // .message // empty)
				][-1] // null
			),
			thread_id: ([.[] | select(.type == "thread.started") | .thread_id][-1] // ""),
			usage: {
				input_tokens: ([.[] | select(.type == "turn.completed") | (.usage.input_tokens // 0)] | add // 0),
				cached_input_tokens: ([.[] | select(.type == "turn.completed") | (.usage.cached_input_tokens // 0)] | add // 0),
				output_tokens: ([.[] | select(.type == "turn.completed") | (.usage.output_tokens // 0)] | add // 0),
				reasoning_output_tokens: ([.[] | select(.type == "turn.completed") | (.usage.reasoning_output_tokens // 0)] | add // 0)
			},
			turns: ([.[] | select(.type == "turn.completed")] | length),
			failure_counts: {
				turn_failures: ([.[] | select(.type == "turn.failed" or .type == "error")] | length),
				executor_process_creation: $executor_process_creation_errors
			},
			item_counts: (
				reduce (.[] | select(.type == "item.completed") | .item.type) as $type
				({}; .[$type] = ((.[$type] // 0) + 1))
			),
			stdout_path: $stdout_path,
			stderr_path: $stderr_path,
			child_events_path: null,
			child_proof_path: null
		}' "$codex_stdout"); then
		task_pass=false
		agent_json=$(jq -cn \
			--argjson exit_code "$exit_code" \
			--argjson canceled "$canceled" \
			--argjson timed_out "$timed_out" \
			--argjson duration_ms "$duration_ms" \
			--argjson executor_process_creation_errors "$executor_process_creation_errors" \
			--arg stdout_path "$codex_stdout" \
			--arg stderr_path "$codex_stderr" '
			{
				exit_code: $exit_code,
				timed_out: $timed_out,
				canceled: $canceled,
				duration_ms: $duration_ms,
				usage: {
					input_tokens: 0,
					cached_input_tokens: 0,
					output_tokens: 0,
					reasoning_output_tokens: 0
				},
				turns: 0,
				failure_counts: {
					turn_failures: 1,
					executor_process_creation: $executor_process_creation_errors
				},
				error: "invalid Codex JSONL",
				stdout_path: $stdout_path,
				stderr_path: $stderr_path,
				child_events_path: null,
				child_proof_path: null
			}') || return 1
	fi
	if [[ -f $child_events ]]; then
		agent_json=$(jq -c --arg path "$child_events" '.child_events_path = $path' <<<"$agent_json") || return 1
	fi
	if [[ -f $child_proof ]]; then
		agent_json=$(jq -c --arg path "$child_proof" '.child_proof_path = $path' <<<"$agent_json") || return 1
	fi
	if [[ -n $expected_final_response ]]; then
		expected_response_required=true
		if ! jq -se --arg expected "$expected_final_response" \
			-f "$benchmark_root/expected_final_response.jq" "$codex_stdout" >/dev/null; then
			expected_response_passed=false
			task_pass=false
		fi
	fi
	if [[ $commentary_coverage_enabled == true ]]; then
		if commentary_coverage_json=$(python3 "$benchmark_root/check_commentary_coverage.py" check \
			"$task_manifest" "$benchmark_mode" "$arm" "$codex_stdout"); then
			commentary_coverage_status=0
		else
			commentary_coverage_status=$?
			task_pass=false
		fi
		if ! jq -e 'type == "object"' <<<"$commentary_coverage_json" >/dev/null 2>&1; then
			commentary_coverage_json=$(jq -cn \
				--arg mode "$benchmark_mode" \
				--arg arm "$arm" \
				--argjson status "$commentary_coverage_status" '
				{
					schema: "mekugi.benchmark.commentary-coverage.v1",
					mode: $mode,
					arm: $arm,
					profiles: [],
					passed: false,
					observed: {agent_messages: 0, successful_commands: 0, item_counts: {}},
					missing: ["checker-error"],
					checker_exit_code: $status
				}') || return 1
			task_pass=false
		fi
		commentary_coverage_json=$(jq -c --arg path "$commentary_coverage_path" \
			'. + {path: $path}' <<<"$commentary_coverage_json") || return 1
		printf '%s\n' "$commentary_coverage_json" >"$commentary_coverage_path" || return 1
	fi

	grader_json=$(jq -cn \
		--argjson passed "$([[ $grader_exit -eq 0 ]] && printf true || printf false)" \
		--argjson exit_code "$grader_exit" \
		--argjson duration_ms "$grader_duration_ms" \
		--arg stdout_path "$artifact_dir/grader-$task_id.stdout" \
		--arg stderr_path "$artifact_dir/grader-$task_id.stderr" \
		--arg grader_name "$grader_name" \
		--argjson expected_response_required "$expected_response_required" \
		--argjson expected_response_passed "$expected_response_passed" '
		[{
			name: $grader_name,
			required: true,
			passed: $passed,
			exit_code: $exit_code,
			timed_out: ($exit_code == 124 or $exit_code == 137),
			duration_ms: $duration_ms,
			stdout_path: $stdout_path,
			stderr_path: $stderr_path
		}] +
		(if $expected_response_required then [{
			name: "decoded-final-response",
			required: true,
			passed: $expected_response_passed,
			exit_code: (if $expected_response_passed then 0 else 1 end),
			timed_out: false,
			duration_ms: 0
		}] else [] end)') || return 1

	# jq variables are supplied by the arguments below.
	# shellcheck disable=SC2016
	result_json=$(jq -cn \
		--arg run_id "$run_id" \
		--arg benchmark_image_id "${benchmark_image_id:-}" \
		--arg arm "$arm" \
		--argjson repetition "$repetition" \
		--argjson order "$order" \
		--arg model "$model" \
		--arg reasoning_effort "$reasoning_effort" \
		--arg parent_model "$root_model" \
		--arg parent_reasoning_effort "$root_reasoning_effort" \
		--argjson mentor_mode "$([[ $benchmark_mode == mentor-handoff ]] && printf true || printf false)" \
		--arg router_mode "$router_mode" \
		--arg started_at "$started_at" \
		--arg task_id "$task_id" \
		--arg task_contract_sha256 "$task_contract_sha256" \
		--arg base_instructions_path "$instruction_path" \
		--arg base_instructions_container_path "/bench-instructions/$instruction_name" \
		--arg base_instructions_sha256 "$instruction_sha" \
		--arg stock_base_instructions_path "$control_instruction" \
		--arg override_diff_path "$instruction_diff_for_arm" \
		--arg override_source_path "$instruction_source" \
		--argjson agent "$agent_json" \
		--argjson changed_paths "$changed_json" \
		--argjson unauthorized_paths "$unauthorized_json" \
		--argjson commentary_coverage "$commentary_coverage_json" \
		--rawfile diff "$diff_path" \
		--arg diff_path "$diff_path" \
		--argjson graders "$grader_json" \
		--argjson task_pass "$task_pass" '
		{
			run_id: $run_id,
			benchmark_image_id: $benchmark_image_id,
			task_id: $task_id,
			task_contract_sha256: $task_contract_sha256,
			arm: $arm,
			repetition: $repetition,
			order_in_block: $order,
			model: $model,
			reasoning_effort: $reasoning_effort,
			parent_model: $parent_model,
			parent_reasoning_effort: $parent_reasoning_effort,
			child_model: (if $mentor_mode then $model else null end),
			child_reasoning_effort: (if $mentor_mode then $reasoning_effort else null end),
			router_mode: $router_mode,
			started_at: $started_at,
			base_instructions: {
				path: $base_instructions_path,
				container_path: $base_instructions_container_path,
				sha256: $base_instructions_sha256,
				stock_path: $stock_base_instructions_path,
				override_diff_path: (if $override_diff_path == "" then null else $override_diff_path end),
				override_source_path: (if $override_diff_path == "" or $override_source_path == "" then null else $override_source_path end)
			},
			agent: $agent,
			changed_paths: $changed_paths,
			unauthorized_paths: $unauthorized_paths,
			commentary_coverage: $commentary_coverage,
			diff: $diff,
			diff_path: $diff_path,
			graders: $graders,
			task_pass: $task_pass
		}') || return 1
	printf '%s\n' "$result_json" >"$result_path" || return 1

	input_tokens=$(jq -r '.usage.input_tokens' <<<"$agent_json") || return 1
	cached_tokens=$(jq -r '.usage.cached_input_tokens' <<<"$agent_json") || return 1
	output_tokens=$(jq -r '.usage.output_tokens' <<<"$agent_json") || return 1
	reasoning_tokens=$(jq -r '.usage.reasoning_output_tokens' <<<"$agent_json") || return 1
	if [[ $task_pass == true ]]; then
		printf 'pass %s: %d ms, input=%s cached=%s output=%s reasoning=%s\n' \
			"$run_id" "$duration_ms" "$input_tokens" "$cached_tokens" "$output_tokens" "$reasoning_tokens"
	else
		printf 'fail %s\n' "$run_id"
	fi
	[[ $task_pass == true ]]
}

# Keep an explicit failure record even when preparation or capture cannot complete.
run_attempt() {
	local status=0 workspace= trusted=
	run_agent "$@" || status=$?
	if [[ -n $trusted ]]; then rm -rf -- "$trusted"; fi
	if [[ -n $workspace ]]; then rm -rf -- "$workspace"; fi
	local run_id
	run_id="$task_id-$1-r$(printf '%03d' "$2")" || return 1
	local artifact_dir="$run_dir/artifacts/$task_id/$run_id"
	if [[ ! -s $artifact_dir/result.json ]]; then
		mkdir -p "$artifact_dir" || return 1
		jq -cn --arg run_id "$run_id" --arg task_id "$task_id" --arg arm "$1" \
			--argjson repetition "$2" --argjson order "$3" \
			'{run_id:$run_id, task_id:$task_id, arm:$arm, repetition:$repetition,
			order_in_block:$order, task_pass:false, infrastructure_error:"mandatory attempt step failed"}' \
			>"$artifact_dir/result.json" || return 1
		status=1
	fi
	return "$status"
}

run_block() {
	local repetition=$1
	local pair_canceled=false pair_cancel_status=143 block_status=0 active_agent_pid=
	local index
	local -a arms=("${run_arms[@]}")
	trap - EXIT
	trap 'cancel_pair 130' INT
	trap 'cancel_pair 143' TERM
	if ((${#arms[@]} == 2 && repetition % 2)); then
		arms=("${run_arms[1]}" "${run_arms[0]}")
	fi
	if ((${#arms[@]} == 1)); then
		run_attempt "${arms[0]}" "$repetition" "$first_arm_order"
		return
	fi
	for index in "${!arms[@]}"; do
		run_attempt "${arms[$index]}" "$repetition" "$((index + first_arm_order))" || block_status=1
		if [[ $pair_canceled == true ]]; then
			return "$pair_cancel_status"
		fi
	done
	trap - INT TERM
	return "$block_status"
}
