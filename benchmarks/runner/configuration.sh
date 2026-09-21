#!/usr/bin/env bash
# Benchmark configuration and normalized arm plan. Sourcing performs no execution.

configure_benchmark() {
	model=${MODEL:-gpt-6-astra}
	reasoning_effort=${REASONING_EFFORT:-medium}
	mentor_parent_model=${MENTOR_PARENT_MODEL:-gpt-5.6-sol}
	mentor_parent_reasoning_effort=high
	mentor_model_protocol=${MENTOR_MODEL_PROTOCOL:-native}
	main_mentor=${BENCHMARK_MAIN_MENTOR:-false}
	diagnostic_model_protocol=${DIAGNOSTIC_MODEL_PROTOCOL:-native}
	mentor_child_role=benchmark_worker
	repetitions=${REPETITIONS:-1}
	benchmark_mode=${BENCHMARK_MODE:-paired}
	prepare_only=${BENCHMARK_PREPARE_ONLY:-false}
	report_issues=${BENCHMARK_REPORT_ISSUES:-false}
	enforce_no_edit_loops=${BENCHMARK_ENFORCE_NO_EDIT_LOOPS:-true}
	control_baseline_dir=${CONTROL_BASELINE_DIR:-}
	case $prepare_only in
	true|false) ;;
	*)
		printf 'bench.sh: BENCHMARK_PREPARE_ONLY must be true or false, got %s\n' "$prepare_only" >&2
		exit 2
		;;
	esac
	case $enforce_no_edit_loops in
	true|false) ;;
	*)
		printf 'bench.sh: BENCHMARK_ENFORCE_NO_EDIT_LOOPS must be true or false, got %s\n' \
			"$enforce_no_edit_loops" >&2
		exit 2
		;;
	esac
	case $report_issues in
	true) export MEKUGI_BENCH_DIAGNOSE=1 ;;
	false) export MEKUGI_BENCH_DIAGNOSE=0 ;;
	*)
		printf 'bench.sh: BENCHMARK_REPORT_ISSUES must be true or false, got %s\n' "$report_issues" >&2
		exit 2
		;;
	esac
	case "$benchmark_mode" in
		paired|ctp-only|mentor-handoff) ;;
		control-only|mekugi-only|mekugi-diagnostic)
			if ((repetitions != 1)); then
				printf 'bench.sh: %s mode requires REPETITIONS=1; run separate trials for independent evidence\n' "$benchmark_mode" >&2
				exit 2
			fi
			;;
		*)
			printf 'bench.sh: BENCHMARK_MODE must be paired, control-only, ctp-only, mentor-handoff, mekugi-only, or mekugi-diagnostic, got %s\n' "$benchmark_mode" >&2
			exit 2
			;;
	esac
	if [[ ($benchmark_mode == control-only || $benchmark_mode == ctp-only || $benchmark_mode == mentor-handoff) && $report_issues != false ]]; then
		printf 'bench.sh: %s mode requires BENCHMARK_REPORT_ISSUES=false so diagnostic reporting does not confound the treatment\n' "$benchmark_mode" >&2
		exit 2
	fi
	if [[ $benchmark_mode == mentor-handoff && $model != gpt-5.6-luna && $model != gpt-5.6-terra ]]; then
		printf 'bench.sh: mentor-handoff mode requires MODEL=gpt-5.6-luna or MODEL=gpt-5.6-terra, got %s\n' "$model" >&2
		exit 2
	fi
	case $mentor_model_protocol in
	native|ctp2) ;;
	*)
		printf 'bench.sh: MENTOR_MODEL_PROTOCOL must be native or ctp2, got %s\n' "$mentor_model_protocol" >&2
		exit 2
		;;
	esac
	case $diagnostic_model_protocol in
	native|ctp2) ;;
	*) printf 'bench.sh: DIAGNOSTIC_MODEL_PROTOCOL must be native or ctp2\n' >&2; exit 2 ;;
	esac
	if [[ -n ${DIAGNOSTIC_MODEL_PROTOCOL+x} && $benchmark_mode != mekugi-diagnostic ]]; then
	    printf 'bench.sh: DIAGNOSTIC_MODEL_PROTOCOL requires mekugi-diagnostic mode\n' >&2
	    exit 2
	fi
	case $main_mentor in
	true)
		if [[ $benchmark_mode != mekugi-diagnostic || ($model != gpt-5.6 && $model != gpt-5.6-sol) ]]; then
			printf 'bench.sh: BENCHMARK_MAIN_MENTOR=true requires mekugi-diagnostic and MODEL=gpt-5.6 or gpt-5.6-sol\n' >&2
			exit 2
		fi
		;;
	false) ;;
	*) printf 'bench.sh: BENCHMARK_MAIN_MENTOR must be true or false\n' >&2; exit 2 ;;
	esac
	task_id=${TASK_ID:-etcd-range-stream}
	suite_manifest="$benchmark_root/diverse-suite.json"
	task=
	task_manifest=
	prompt_file=
	task_contract_sha256=
	source_repo=
	source_repository=
	source_is_public=false
	source_kind=git
	base_commit=
	oracle_commit=
	allowed_paths=()
	hidden_sources=()
	hidden_paths=()
	grader_command=()
	grader_name=
	baseline_output_contains=
	dependency_kind=none
	preload_go_qualification_grader=false
	require_ctp_input_compression=false
	require_ctp_output_compression=false
	expected_final_response=
	commentary_coverage_enabled=false

	agent_timeout=
	grader_timeout=

	configure_benchmark_plan
}

configure_benchmark_plan() {
	local arm
	run_arms=(control mekugi)
	imported_arms=()
	first_arm_order=1
	export MEKUGI_BENCH_MEKUGI_MODEL_PROTOCOL=native
	case $benchmark_mode in
	paired) MEKUGI_BENCH_MEKUGI_MODEL_PROTOCOL=ctp2 ;;
	control-only) run_arms=(control) ;;
	mekugi-only) run_arms=(mekugi); imported_arms=(control); first_arm_order=2 ;;
	mekugi-diagnostic) run_arms=(mekugi); MEKUGI_BENCH_MEKUGI_MODEL_PROTOCOL=$diagnostic_model_protocol ;;
	ctp-only) run_arms=(native ctp); MEKUGI_BENCH_MEKUGI_MODEL_PROTOCOL=ctp2 ;;
	mentor-handoff) run_arms=(mekugi mekugi-mentor); MEKUGI_BENCH_MEKUGI_MODEL_PROTOCOL=$mentor_model_protocol ;;
	esac
	retained_arms=("${imported_arms[@]}" "${run_arms[@]}")
	declare -gA arm_services=() arm_modes=() arm_protocols=() arm_instructions=()
	declare -gA arm_metrics=() arm_captures=() arm_mentor=()
	for arm in "${retained_arms[@]}"; do
		arm_services[$arm]=mekugi-agent
		arm_modes[$arm]=mekugi
		arm_protocols[$arm]=$MEKUGI_BENCH_MEKUGI_MODEL_PROTOCOL
		arm_instructions[$arm]=mekugi.md
		arm_metrics[$arm]=mekugi-metrics.json
		arm_captures[$arm]=captures/mekugi.jsonl
		arm_mentor[$arm]=false
		case $arm in
		control|native)
			arm_services[$arm]=control-agent
			arm_protocols[$arm]=native
			arm_metrics[$arm]=control-metrics.json
			arm_captures[$arm]=captures/control.jsonl
			if [[ $arm == control ]]; then
				arm_modes[$arm]=passthrough
				arm_instructions[$arm]=control.md
			fi
			;;
		mekugi)
			if [[ $benchmark_mode == mentor-handoff ]]; then
				arm_services[$arm]=control-agent
				arm_captures[$arm]=captures/control.jsonl
			fi
			;;
		mekugi-mentor)
			arm_metrics[$arm]=mekugi-mentor-metrics.json
			arm_mentor[$arm]=true
			;;
		esac
	done
}

initialize_run() {
	if [[ $benchmark_mode == mekugi-only && -z $control_baseline_dir ]]; then
		printf 'bench.sh: mekugi-only requires CONTROL_BASELINE_DIR from a current control-only run\n' >&2
		exit 2
	fi
	results_root="$benchmark_root/results"
	mkdir -p "$results_root"
	if ! benchmark_commit=$(git -C "$benchmark_root/.." rev-parse HEAD); then
		printf 'bench.sh: cannot determine benchmark commit\n' >&2
		exit 1
	fi
	if ! codex_release=$(sed -n 's/^ARG CODEX_RELEASE=//p' "$benchmark_root/Dockerfile") || [[ -z $codex_release ]]; then
		printf 'bench.sh: cannot determine pinned Codex CLI release\n' >&2
		exit 1
	fi
	run_dir=$(mktemp -d "$results_root/.staging-XXXXXX")
	dependency_cache=$(mktemp -d "$results_root/.dependency-cache-XXXXXX")
	chmod 0750 "$dependency_cache"
	dependency_workspace=

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
	instruction_source=
	mentor_parent_prompt="$instruction_dir/mentor-parent.md"
	mentor_child_prompt="$instruction_dir/mentor-child.md"
	mentor_child_role_config="$instruction_dir/mentor-child.toml"
	mentor_spawn_prompt="$instruction_dir/mentor-spawn-message.txt"
	run_suffix=$(basename "$run_dir")
	image_tag_prefix=${MEKUGI_BENCH_IMAGE_TAG:-run}
	export MEKUGI_BENCH_IMAGE_TAG="$image_tag_prefix-${run_suffix#.staging-}"
	benchmark_image="mekugi-bench:$MEKUGI_BENCH_IMAGE_TAG"
	control_instruction_sha=
	mekugi_instruction_sha=
	result_files=()
	worker_pids=()
	started=false
	compose_used=false
	collected=false

	export BENCH_RUN_DIR=$run_dir
	export BENCH_DEPENDENCY_CACHE=$dependency_cache
	export CODEX_AUTH_PATH=${CODEX_AUTH_PATH:-${CODEX_HOME:-$HOME/.codex}/auth.json}
	compose_project_name="mekugi_bench_$(basename "$run_dir" | tr '[:upper:]' '[:lower:]' | tr -cd '[:alnum:]_')"
	export COMPOSE_PROJECT_NAME=$compose_project_name
	export MEKUGI_BENCH_COMPOSE_FILE="$benchmark_root/compose.yaml"
	compose=(docker compose --progress quiet -f "$MEKUGI_BENCH_COMPOSE_FILE")
}
