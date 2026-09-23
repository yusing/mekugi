#!/usr/bin/env bash
set -euo pipefail

if (($# != 1)); then
	printf 'usage: %s RUN_DIRECTORY\n' "${0##*/}" >&2
	exit 2
fi

run_dir=$1
benchmark_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
for executable in jq python3; do
	if ! command -v "$executable" >/dev/null; then
		printf 'report.sh: %s is required\n' "$executable" >&2
		exit 1
	fi
done

results="$run_dir/results.jsonl"
config="$run_dir/benchmark-config.json"
summary="$run_dir/summary.md"
temporary="$summary.tmp"
mode=paired
[[ -s $config ]] && mode=$(jq -r '.benchmark_mode // "paired"' "$config")

baseline_arm=control
baseline_label=Control
baseline_metrics="$run_dir/control-metrics.json"
baseline_capture="$run_dir/captures/control.jsonl"
treatment_arm=mekugi
treatment_label=Mekugi
treatment_metrics="$run_dir/mekugi-metrics.json"
treatment_capture="$run_dir/captures/mekugi.jsonl"
case "$mode" in
	control-only)
		baseline_arm=
		baseline_label=
		baseline_metrics=
		baseline_capture=
		treatment_arm=control
		treatment_label=Stock
		treatment_metrics="$run_dir/control-metrics.json"
		treatment_capture="$run_dir/captures/control.jsonl"
		;;
	mentor-handoff)
		baseline_arm=mekugi
		baseline_label=Mekugi
		treatment_arm=mekugi-mentor
		treatment_label='Mekugi + Mentor Handoff'
		baseline_metrics="$run_dir/mekugi-metrics.json"
		treatment_metrics="$run_dir/mekugi-mentor-metrics.json"
		;;
	mekugi-only|mekugi-diagnostic)
		baseline_arm=
		baseline_label=
		baseline_metrics=
		baseline_capture=
		;;
	paired) baseline_label=Stock ;;
	*) printf 'report.sh: unsupported benchmark mode: %s\n' "$mode" >&2; exit 1 ;;
esac

for file in "$results" "$treatment_metrics" "$treatment_capture"; do
	if [[ ! -s $file ]]; then
		printf 'report.sh: required benchmark artifact is missing or empty: %s\n' "$file" >&2
		exit 1
	fi
done

analysis_dir=$(mktemp -d)
trap 'rm -rf -- "$analysis_dir"' EXIT
python3 "$benchmark_root/analyze_capture.py" \
	"$treatment_metrics" "$treatment_capture" "$results" "$treatment_arm" \
	>"$analysis_dir/treatment-validation.json"

has_baseline=false
if [[ -n $baseline_metrics ]]; then
	for file in "$baseline_metrics" "$baseline_capture"; do
		if [[ ! -s $file ]]; then
			printf 'report.sh: required baseline artifact is missing or empty: %s\n' "$file" >&2
			exit 1
		fi
	done
	python3 "$benchmark_root/analyze_capture.py" \
		"$baseline_metrics" "$baseline_capture" "$results" "$baseline_arm" \
		>"$analysis_dir/baseline-validation.json"
	has_baseline=true
fi

result_value() {
	local arm=$1
	local expression=$2
	jq -sr --arg arm "$arm" "[.[] | select(.arm == \$arm)] | $expression" "$results"
}

metric() {
	local file=$1
	local expression=$2
	jq -r "$expression" "$file"
}

percentage() {
	local numerator=$1
	local denominator=$2
	if ((denominator == 0)); then
		printf 'n/a'
	else
		awk -v n="$numerator" -v d="$denominator" 'BEGIN { printf "%.2f%%", 100*n/d }'
	fi
}

rate() {
	local value=$1
	if [[ $value == null ]]; then
		printf 'n/a'
	else
		awk -v value="$value" 'BEGIN { printf "%.2f%%", 100*value }'
	fi
}

print_outcome_row() {
	local label=$1
	local arm=$2
	local metrics=$3
	local passes runs duration
	passes=$(result_value "$arm" 'map(select(.task_pass == true)) | length')
	runs=$(result_value "$arm" 'length')
	duration=$(result_value "$arm" 'map(.agent.duration_ms // 0) | add // 0')
	printf '| %s | %s/%s | %.3f | %s | %s | %s | %s |\n' \
		"$label" "$passes" "$runs" "$(awk -v ms="$duration" 'BEGIN {print ms/1000}')" \
		"$(metric "$metrics" '.requests.logical')" \
		"$(metric "$metrics" '.usage.input_tokens')" \
		"$(metric "$metrics" '.usage.output_tokens')" \
		"$(metric "$metrics" '.usage.reasoning_tokens')"
}

print_capture_rows() {
	local label=$1
	local metrics=$2
	printf '| %s | %s | %s | %s | %s | %s | %s | %s |\n' \
		"$label" \
		"$(metric "$metrics" '.capture.records')" \
		"$(metric "$metrics" '.requests.provider_attempts')" \
		"$(metric "$metrics" '.capture.capture_errors')" \
		"$(metric "$metrics" '.capture.incomplete_records')" \
		"$(metric "$metrics" '[.capture.missing_provider_records, .capture.provider_attempt_gaps] | add')" \
		"$(metric "$metrics" '[.capture.write_errors, .capture.skipped_requests] | add')" \
		"$(metric "$metrics" '.capture.dropped_exchange_details')"
}

print_tool_rows() {
	local label=$1
	local metrics=$2
	jq -r --arg label "$label" '
		([.provider_tools, .delivered_tools] | map(keys) | add | unique)[] as $name |
		[$label, $name,
		 (.provider_tools[$name].calls // 0), (.provider_tools[$name].input_tokens // 0),
		 (.delivered_tools[$name].calls // 0), (.delivered_tools[$name].input_tokens // 0)] |
		"| \(.[0]) | `\(.[1])` | \(.[2]) | \(.[3]) | \(.[4]) | \(.[5]) |"
	' "$metrics"
}

print_model_rows() {
	local label=$1
	local metrics=$2
	jq -r --arg label "$label" '
		[.exchanges[].provider_attempts[] |
		 {model: (.model // "unknown"), usage: .usage}] |
		group_by(.model)[] |
		[$label, .[0].model, length,
		 (map(select(.usage != null)) | length),
		 (map(.usage.input_tokens // 0) | add),
		 (map(.usage.cached_input_tokens // 0) | add),
		 (map(.usage.output_tokens // 0) | add),
		 (map(.usage.reasoning_tokens // 0) | add)] |
		"| \(.[0]) | `\(.[1])` | \(.[2]) | \(.[3]) | \(.[4]) | \(.[5]) | \(.[6]) | \(.[7]) |"
	' "$metrics"
}

task_id=$(jq -sr '.[0].task_id' "$results")
model=$(jq -sr '.[0] | .parent_model // .model' "$results")
effort=$(jq -sr '.[0] | .parent_reasoning_effort // .reasoning_effort' "$results")
treatment_input=$(metric "$treatment_metrics" '.usage.input_tokens')
treatment_output=$(metric "$treatment_metrics" '.usage.output_tokens')

{
	printf '# Mekugi benchmark: %s\n\n' "$task_id"
	printf -- '- Mode: `%s`\n' "$mode"
	printf -- '- Model: `%s`\n' "$model"
	printf -- '- Reasoning effort: `%s`\n' "$effort"
	if [[ $(jq -r '.main_mentor.enabled // false' "$config") == true ]]; then
		printf -- '- Main Mentor Handoff: `%s` → `%s` (`%s` configured reasoning)\n' \
			"$(jq -r '.main_mentor.model' "$config")" \
			"$(jq -r '.main_mentor.requested_model' "$config")" \
			"$(jq -r '.main_mentor.requested_reasoning_effort' "$config")"
	fi
	if [[ $mode == mentor-handoff ]]; then
		printf -- '- Requested child model: `%s`; initial mentor model: `%s`\n' \
			"$(jq -sr '.[0].child_model' "$results")" \
			"$(jq -r '.mentor_handoff.mentor_model' "$config")"
	fi
	printf -- '- Metrics owner: in-process `capturer` on each router listener\n'
	printf -- '- Evidence validation: passed\n'

	printf '\n## Outcome and provider usage\n\n'
	printf '| Arm | Task passes | Agent wall time (s) | Logical requests | Input tokens | Output tokens | Reasoning tokens |\n'
	printf '|---|---:|---:|---:|---:|---:|---:|\n'
	if [[ $has_baseline == true ]]; then
		print_outcome_row "$baseline_label" "$baseline_arm" "$baseline_metrics"
	fi
	print_outcome_row "$treatment_label" "$treatment_arm" "$treatment_metrics"
	if [[ $has_baseline == true ]]; then
		baseline_input=$(metric "$baseline_metrics" '.usage.input_tokens')
		baseline_output=$(metric "$baseline_metrics" '.usage.output_tokens')
		printf '\nActual provider-token change from %s to %s: input **%+d** (%s), output **%+d** (%s).\n' \
			"$baseline_label" "$treatment_label" \
			"$((treatment_input - baseline_input))" "$(percentage "$((treatment_input - baseline_input))" "$baseline_input")" \
			"$((treatment_output - baseline_output))" "$(percentage "$((treatment_output - baseline_output))" "$baseline_output")"
	fi

	printf '\n## Cache attribution\n\n'
	printf '| Arm | Cached input | Uncached input | Provider cache rate | Cold/new uncached | Eligible prefix | Eligible cached | Eligible misses | Eligible prefix cache rate |\n'
	printf '|---|---:|---:|---:|---:|---:|---:|---:|---:|\n'
	for row in treatment ${has_baseline/true/baseline}; do
		[[ $row == false ]] && continue
		if [[ $row == baseline ]]; then label=$baseline_label; metrics=$baseline_metrics; else label=$treatment_label; metrics=$treatment_metrics; fi
		eligible=$(metric "$metrics" '.cache.eligible_prefix_tokens')
		eligible_cached=$(metric "$metrics" '.cache.eligible_prefix_cached_tokens')
		printf '| %s | %s | %s | %s | %s | %s | %s | %s | %s |\n' \
			"$label" "$(metric "$metrics" '.usage.cached_input_tokens')" \
			"$(metric "$metrics" '.usage.uncached_input_tokens')" \
			"$(rate "$(metric "$metrics" '.cache.provider_cache_rate')")" \
			"$(metric "$metrics" '.cache.cold_or_new_uncached_input_tokens')" \
			"$eligible" "$eligible_cached" \
			"$(metric "$metrics" '.cache.eligible_prefix_miss_tokens')" \
			"$(rate "$(metric "$metrics" '.cache.eligible_prefix_cache_rate')")"
	done

	printf '\n## Cache-prefix diagnostics\n\n'
		printf 'Comparisons use decoded request items, not the provider hidden token prefix. Appended/identical means the observed earlier content is stable; it does not guarantee a cache hit. A changed projected prefix with a stable client prefix points to projection/replay. Missing, truncated, restarted, or first prefix observations are unavailable. Routing compares private fingerprints of the actual outgoing session key. Turn-state forwarding compares the current client/provider sticky-routing header: absent, preserved, dropped, changed, or unavailable. Stable session keys alone do not prove sticky routing; no key or content hash is shown.\n\n'
		printf '| Arm | Request ordinal | Input | Cached | Client prefix | Projected prefix | Provider prefix | Route key | Request cache key | Turn-state forwarding |\n'
	printf '|---|---:|---:|---:|---|---|---|---|---|---|\n'
	for row in treatment ${has_baseline/true/baseline}; do
		[[ $row == false ]] && continue
		metrics=$treatment_metrics label=$treatment_label
		if [[ $row == baseline ]]; then metrics=$baseline_metrics; label=$baseline_label; fi
		jq -r --arg arm "$label" '
          def prefix: if . == null then "unavailable" else .status + (if .status == "changed" then " (common items=" + (.common_items|tostring) + (if (.changed_fields|length)>0 then "; fields=" + (.changed_fields|join(",")) else "" end) + ")" else "" end) end;
          .exchanges | sort_by(.sequence) | to_entries[] | .key as $ordinal | .value as $e |
          $e.provider_attempts[-1] as $p | $e.cache_diagnostics as $d |
	          "| \($arm) | \($ordinal+1) | \($p.usage.input_tokens // "n/a") | \($p.usage.cached_input_tokens // "n/a") | \($d.client | prefix) | \($d.projected | prefix) | \($d.provider | prefix) | \($d.routing // "unavailable") | \($d.request_key // "unavailable") | \($d.turn_state_forwarding // "unavailable") |"
        ' "$metrics"
	done


    printf '\n## Provider response evidence\n\n'
    printf 'Cached-token telemetry distinguishes an explicit count (including zero) from missing, null, invalid, or unavailable evidence. Legacy aggregate counters may default missing telemetry to zero; those zeros are not proven cache misses. Response/header models are provider-reported identifiers, not verification of backend identity. Provider request IDs are retained privately in capture details, not this summary.\n\n'
    printf '| Arm | Request ordinal | Attempt | Response model | Header model | Cached-token telemetry | Explicit cached tokens |\n'
    printf '|---|---:|---:|---|---|---|---:|\n'
    for row in treatment ${has_baseline/true/baseline}; do
        [[ $row == false ]] && continue
        metrics=$treatment_metrics label=$treatment_label
        if [[ $row == baseline ]]; then metrics=$baseline_metrics; label=$baseline_label; fi
        jq -r --arg arm "$label" '
          .exchanges | sort_by(.sequence) | to_entries[] | .key as $ordinal | .value.provider_attempts[] |
          .provider_response as $e |
          "| \($arm) | \($ordinal+1) | \(.attempt) | \($e.model // "unavailable") | \($e.header_model // "unavailable") | \($e.cached_tokens_state // "unavailable") | \(if $e.cached_tokens_state == "present" then $e.cached_tokens else "unavailable" end) |"
        ' "$metrics"
    done

	printf '\n## Observed payload-token estimates\n\n'
	printf 'These local estimates measure observed request and response representations. They are not provider usage or billed tokens. Retries remain separate provider attempts.\n\n'
	printf '| Arm | Client requests | Provider requests | Provider response streams | Client response streams | Provider outputs | Client outputs |\n'
	printf '|---|---:|---:|---:|---:|---:|---:|\n'
	for row in treatment ${has_baseline/true/baseline}; do
		[[ $row == false ]] && continue
		if [[ $row == baseline ]]; then label=$baseline_label; metrics=$baseline_metrics; else label=$treatment_label; metrics=$treatment_metrics; fi
		printf '| %s | %s | %s | %s | %s | %s | %s |\n' \
			"$label" \
			"$(metric "$metrics" '.transport.client_requests.tokens')" \
			"$(metric "$metrics" '.transport.provider_attempt_requests.tokens')" \
			"$(metric "$metrics" '.transport.provider_responses.tokens')" \
			"$(metric "$metrics" '.transport.client_responses.tokens')" \
			"$(metric "$metrics" '.semantic.provider_attempt_outputs.tokens')" \
			"$(metric "$metrics" '.semantic.client_outputs.tokens')"
	done
	printf '\n## Actual model use\n\n'
	printf '| Arm | Provider model | Provider attempts | Usage-bearing attempts | Input tokens | Cached input | Output tokens | Reasoning tokens |\n'
	printf '|---|---|---:|---:|---:|---:|---:|---:|\n'
	if [[ $has_baseline == true ]]; then print_model_rows "$baseline_label" "$baseline_metrics"; fi
	print_model_rows "$treatment_label" "$treatment_metrics"

	printf '\n## Tool transport\n\n'
	printf '| Arm | Tool | Provider calls | Provider input tokens | Delivered calls | Delivered input tokens |\n'
	printf '|---|---|---:|---:|---:|---:|\n'
	if [[ $has_baseline == true ]]; then print_tool_rows "$baseline_label" "$baseline_metrics"; fi
	print_tool_rows "$treatment_label" "$treatment_metrics"

	printf '\n## Capture completeness\n\n'
	printf '| Arm | Records | Provider attempts | Capture errors | Incomplete | Provider/sequence errors | Write/skipped errors | Dropped detail |\n'
	printf '|---|---:|---:|---:|---:|---:|---:|---:|\n'
	if [[ $has_baseline == true ]]; then print_capture_rows "$baseline_label" "$baseline_metrics"; fi
	print_capture_rows "$treatment_label" "$treatment_metrics"

	printf '\nThe capturer snapshot is authoritative for calculations. `results.jsonl` is reconciled against per-thread provider usage, and the sanitized schema-7 JSONL in `captures/` is reconciled against snapshot health and exchange totals. The summary contains no request, session, thread, call, or capture identifiers.\n'
} >"$temporary"

mv -f -- "$temporary" "$summary"
printf 'Benchmark summary: %s\n' "$summary"
