#!/usr/bin/env bash
set -euo pipefail

benchmark_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
fixture=$(mktemp -d)
trap 'rm -rf -- "$fixture"' EXIT
mkdir -p "$fixture/captures"

cat >"$fixture/benchmark-config.json" <<'JSON'
{"benchmark_mode":"paired"}
JSON
cat >"$fixture/results.jsonl" <<'JSONL'
{"task_id":"fixture","arm":"control","model":"model","reasoning_effort":"high","task_pass":true,"agent":{"thread_id":"thread-control","duration_ms":1000,"usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20,"reasoning_output_tokens":5}}}
{"task_id":"fixture","arm":"mekugi","model":"model","reasoning_effort":"high","task_pass":true,"agent":{"thread_id":"thread-mekugi","duration_ms":900,"usage":{"input_tokens":80,"cached_input_tokens":50,"output_tokens":12,"reasoning_output_tokens":3}}}
JSONL

write_capture() {
	local path=$1 id=$2 thread=$3 input=$4 cached=$5 output=$6 reasoning=$7 mode=$8
	cat >"$path" <<JSONL
{"schema_version":6,"boundary":"provider","capture_id":"$id","request_sequence":1,"provider_attempt":1,"mode":"$mode","model_protocol":"native","thread_id":"$thread","request_model":"model","request":{"bytes":80,"tokens":20},"native_request":{"bytes":80,"tokens":20},"status_code":200,"response_complete":true,"response_status":"completed","usage":{"input_tokens":$input,"cached_input_tokens":$cached,"output_tokens":$output,"reasoning_tokens":$reasoning},"tool_calls":[{"call_id":"call-1","name":"hpatch","input_bytes":10,"input_tokens":3,"item_bytes":30,"item_tokens":8}],"response":{"bytes":90,"tokens":22},"final_output":{"bytes":70,"tokens":17},"final_text":{"bytes":70,"tokens":17},"duration_ms":10,"captured_at":"2026-08-28T00:00:00Z"}
{"schema_version":6,"boundary":"codex","capture_id":"$id","request_sequence":1,"mode":"$mode","model_protocol":"native","thread_id":"$thread","request_model":"model","request":{"bytes":100,"tokens":25},"status_code":200,"response_complete":true,"response_status":"completed","tool_calls":[{"call_id":"call-1","name":"exec","input_bytes":30,"input_tokens":9,"item_bytes":50,"item_tokens":14,"kind":"apply_patch"}],"response":{"bytes":120,"tokens":30},"final_output":{"bytes":90,"tokens":25},"final_text":{"bytes":70,"tokens":17},"duration_ms":12,"captured_at":"2026-08-28T00:00:00Z"}
JSONL
}

write_metrics() {
	local path=$1 thread=$2 input=$3 cached=$4 output=$5 reasoning=$6 mode=$7
	local cache_rate
	cache_rate=$(awk -v cached="$cached" -v input="$input" 'BEGIN { print cached/input }')
	cat >"$path" <<JSON
{"schema":"mekugi.capture.metrics.v4","mode":"$mode","model_protocol":"native","requests":{"logical":1,"provider_attempts":1,"completed":1,"failed":0},"usage":{"input_tokens":$input,"cached_input_tokens":$cached,"uncached_input_tokens":$((input-cached)),"output_tokens":$output,"reasoning_tokens":$reasoning,"provider_attempts":1},"cache":{"cold_or_new_uncached_input_tokens":$((input-cached)),"provider_cache_rate":$cache_rate,"eligible_prefix_tokens":0,"eligible_prefix_cached_tokens":0,"eligible_prefix_miss_tokens":0,"eligible_prefix_cache_rate":null},"transport":{"client_control_requests":{"bytes":0,"tokens":0},"client_requests":{"bytes":100,"tokens":25},"provider_attempt_requests":{"bytes":80,"tokens":20},"provider_control_requests":{"bytes":0,"tokens":0},"provider_control_responses":{"bytes":0,"tokens":0},"provider_responses":{"bytes":90,"tokens":22},"client_control_responses":{"bytes":0,"tokens":0},"client_responses":{"bytes":120,"tokens":30}}
,"semantic":{"provider_attempt_outputs":{"bytes":70,"tokens":17},"client_outputs":{"bytes":90,"tokens":25}},"protocol":{"input_payload_tokens_saved":0,"input_payload_bytes_saved":0,"output_text_tokens_saved":0,"output_payload_tokens_expansion":8,"output_payload_bytes_expansion":20},"provider_tools":{"hpatch":{"calls":1,"input_bytes":10,"input_tokens":3,"item_bytes":30,"item_tokens":8}},"delivered_tools":{"exec":{"calls":1,"input_bytes":30,"input_tokens":9,"item_bytes":50,"item_tokens":14}},"mekugi":{"calls":1,"corrections":0,"successful":1,"rejected":0,"unmatched":0,"provider_input_tokens":3,"delivered_input_tokens":9,"carrier_input_tokens_expansion":6},"exchanges":[{"sequence":1,"thread_id":"$thread","model":"model","provider_attempts":[{"attempt":1,"model":"model","status":"completed","response_complete":true,"usage":{"input_tokens":$input,"cached_input_tokens":$cached,"uncached_input_tokens":$((input-cached)),"output_tokens":$output,"reasoning_tokens":$reasoning,"provider_attempts":1},"request":{"bytes":80,"tokens":20},"native_request":{"bytes":80,"tokens":20},"response":{"bytes":90,"tokens":22},"final_output":{"bytes":70,"tokens":17},"final_text":{"bytes":70,"tokens":17},"tools":[{"call_id":"call-1","name":"hpatch","input_bytes":10,"input_tokens":3,"item_bytes":30,"item_tokens":8}]}],"status":"completed","usage":{"input_tokens":$input,"cached_input_tokens":$cached,"uncached_input_tokens":$((input-cached)),"output_tokens":$output,"reasoning_tokens":$reasoning,"provider_attempts":1},"client_request":{"bytes":100,"tokens":25},"client_response":{"bytes":120,"tokens":30},"client_final_output":{"bytes":90,"tokens":25},"client_final_text":{"bytes":70,"tokens":17},"delivered_tools":[{"call_id":"call-1","name":"exec","input_bytes":30,"input_tokens":9,"item_bytes":50,"item_tokens":14,"kind":"apply_patch"}]}],"capture":{"records":2,"capture_errors":0,"incomplete_records":0,"missing_provider_records":0,"provider_attempt_gaps":0,"write_errors":0,"skipped_requests":0,"dropped_exchange_details":0}}
JSON
}

write_capture "$fixture/captures/control.jsonl" control-id thread-control 100 40 20 5 passthrough
write_capture "$fixture/captures/mekugi.jsonl" mekugi-id thread-mekugi 80 50 12 3 mekugi
write_metrics "$fixture/control-metrics.json" thread-control 100 40 20 5 passthrough
write_metrics "$fixture/mekugi-metrics.json" thread-mekugi 80 50 12 3 mekugi

# A matched carrier without outcome evidence is not a rejected edit.
for carrier_kind in other '' exec_command; do
    jq -c --arg kind "$carrier_kind" \
        'if .boundary == "codex" then .tool_calls[0].kind = $kind else . end' \
        "$fixture/captures/mekugi.jsonl" >"$fixture/unknown-carrier.jsonl"
    jq --arg kind "$carrier_kind" \
        '.exchanges[0].delivered_tools[0].kind = $kind | .mekugi.successful = 0 | .mekugi.unclassified = 1' \
        "$fixture/mekugi-metrics.json" >"$fixture/unknown-carrier-metrics.json"
    python3 "$benchmark_root/analyze_capture.py" "$fixture/unknown-carrier-metrics.json" \
        "$fixture/unknown-carrier.jsonl" "$fixture/results.jsonl" mekugi >/dev/null
    jq '.mekugi.rejected = 1 | .mekugi.unclassified = 0' \
        "$fixture/unknown-carrier-metrics.json" >"$fixture/false-rejection.json"
    if python3 "$benchmark_root/analyze_capture.py" "$fixture/false-rejection.json" \
        "$fixture/unknown-carrier.jsonl" "$fixture/results.jsonl" mekugi >/dev/null 2>&1; then
        echo "unknown carrier was accepted as a rejection" >&2
        exit 1
    fi
    jq 'del(.mekugi.unclassified)' "$fixture/unknown-carrier-metrics.json" >"$fixture/missing-outcome.json"
    if python3 "$benchmark_root/analyze_capture.py" "$fixture/missing-outcome.json" \
        "$fixture/unknown-carrier.jsonl" "$fixture/results.jsonl" mekugi >/dev/null 2>&1; then
        echo "unknown carrier was accepted without its counter" >&2
        exit 1
    fi
done

kind_capture="$fixture/request-kind.jsonl"
kind_metrics="$fixture/request-kind-metrics.json"
jq -c '.request_kind = "compaction"' "$fixture/captures/mekugi.jsonl" >"$kind_capture"
jq '.exchanges[].request_kind = "compaction"' "$fixture/mekugi-metrics.json" >"$kind_metrics"
python3 "$benchmark_root/analyze_capture.py" "$kind_metrics" "$kind_capture" "$fixture/results.jsonl" mekugi >/dev/null
jq '.exchanges[].request_kind = "turn"' "$kind_metrics" >"$fixture/wrong-kind-metrics.json"
if python3 "$benchmark_root/analyze_capture.py" "$fixture/wrong-kind-metrics.json" "$kind_capture" "$fixture/results.jsonl" mekugi >/dev/null 2>&1; then
    printf 'capture validator accepted inconsistent request kind\n' >&2; exit 1
fi
for invalid_kind in turn 'private metadata'; do
    jq -c --arg kind "$invalid_kind" 'if .boundary == "codex" then .request_kind = $kind else . end' "$kind_capture" >"$fixture/wrong-kind-capture.jsonl"
    if python3 "$benchmark_root/analyze_capture.py" "$kind_metrics" "$fixture/wrong-kind-capture.jsonl" "$fixture/results.jsonl" mekugi >/dev/null 2>&1; then
        printf 'capture validator accepted altered request kind\n' >&2; exit 1
    fi
done

bash "$benchmark_root/report.sh" "$fixture" >/dev/null
control_capture="$fixture/control-traffic.jsonl"
control_metrics="$fixture/control-traffic-metrics.json"
cp "$fixture/captures/mekugi.jsonl" "$control_capture"
printf '%s\n' '{"schema_version":6,"boundary":"codex_control","control_direction":"request","capture_id":"control-id","mode":"mekugi","model_protocol":"native","response_complete":true,"request":{"bytes":11,"tokens":3}}' >>"$control_capture"
jq '.capture.records += 1 | .transport.client_control_requests = {bytes: 11, tokens: 3}' \
    "$fixture/mekugi-metrics.json" >"$control_metrics"
python3 "$benchmark_root/analyze_capture.py" "$control_metrics" "$control_capture" "$fixture/results.jsonl" mekugi >/dev/null
jq '.transport.client_control_requests.bytes += 1' "$control_metrics" >"$fixture/bad-control-traffic.json"
if python3 "$benchmark_root/analyze_capture.py" "$fixture/bad-control-traffic.json" \
    "$control_capture" "$fixture/results.jsonl" mekugi >/dev/null 2>&1; then
    printf 'capture validator accepted unreconciled control traffic\n' >&2
    exit 1
fi
prewarm_capture="$fixture/prewarm-capture.jsonl"
jq -c 'if .boundary == "codex" then .provider_expected = false else . end' \
    "$fixture/captures/mekugi.jsonl" >"$prewarm_capture"
provider_free_capture="$fixture/provider-free-prewarm-capture.jsonl"
provider_free_metrics="$fixture/provider-free-prewarm-metrics.json"
provider_free_results="$fixture/provider-free-prewarm-results.jsonl"
missing_provider_expectation_capture="$fixture/missing-provider-expectation-capture.jsonl"
jq -c 'select(.boundary != "provider") | .provider_expected = false' \
    "$fixture/captures/mekugi.jsonl" >"$provider_free_capture"
jq '.requests.provider_attempts = 0 |
    .usage = {
        input_tokens: 0, cached_input_tokens: 0, uncached_input_tokens: 0,
        output_tokens: 0, reasoning_tokens: 0, provider_attempts: 0
    } |
    .cache = {
        cold_or_new_uncached_input_tokens: 0, provider_cache_rate: null,
        eligible_prefix_tokens: 0, eligible_prefix_cached_tokens: 0,
        eligible_prefix_miss_tokens: 0, eligible_prefix_cache_rate: null
    } |
    .transport.provider_attempt_requests = {bytes: 0, tokens: 0} |
    .transport.provider_responses = {bytes: 0, tokens: 0} |
    .semantic.provider_attempt_outputs = {bytes: 0, tokens: 0} |
    .protocol = {
        input_payload_tokens_saved: 0, input_payload_bytes_saved: 0,
        output_text_tokens_saved: 0, output_payload_tokens_expansion: 0,
        output_payload_bytes_expansion: 0
    } |
    .provider_tools = {} |
    .mekugi = {
        calls: 0, corrections: 0, successful: 0, rejected: 0, unmatched: 0,
        provider_input_tokens: 0, delivered_input_tokens: 0,
        carrier_input_tokens_expansion: 0
    } |
    .exchanges[0].provider_attempts = [] |
    del(.exchanges[0].usage) |
    .capture.records = 1' \
    "$fixture/mekugi-metrics.json" >"$provider_free_metrics"
jq -c 'select(.arm == "mekugi") |
    .agent.usage = {
        input_tokens: 0, cached_input_tokens: 0, output_tokens: 0,
        reasoning_output_tokens: 0
    }' "$fixture/results.jsonl" >"$provider_free_results"
python3 "$benchmark_root/analyze_capture.py" \
    "$provider_free_metrics" "$provider_free_capture" "$provider_free_results" mekugi >/dev/null
jq -c 'select(.boundary != "provider")' \
    "$fixture/captures/mekugi.jsonl" >"$missing_provider_expectation_capture"
PYTHONPATH="$benchmark_root" python3 - \
    "$fixture/mekugi-metrics.json" \
    "$prewarm_capture" \
    "$fixture/results.jsonl" \
    "$provider_free_metrics" \
    "$provider_free_capture" \
    "$missing_provider_expectation_capture" <<'PY'

from pathlib import Path
import sys

from analyze_capture import load_json, validate_raw_capture, validate_results

metrics = load_json(Path(sys.argv[1]))
excluded = validate_raw_capture(Path(sys.argv[2]), metrics)
if excluded != {1}:
    raise SystemExit(f"prewarm exclusion = {excluded}")
provider_free_metrics = load_json(Path(sys.argv[4]))
provider_free_excluded = validate_raw_capture(Path(sys.argv[5]), provider_free_metrics)
if provider_free_excluded != {1}:
    raise SystemExit(f"provider-free prewarm exclusion = {provider_free_excluded}")
try:
    validate_raw_capture(Path(sys.argv[6]), provider_free_metrics)
except ValueError:
    pass
else:
    raise SystemExit("capture validator accepted a missing provider without an explicit exception")


ordinary = {
    "sequence": 2,
    "thread_id": "thread-mekugi",
    "provider_attempts": [{"model": "model"}],
    "usage": {
        "input_tokens": 80,
        "cached_input_tokens": 50,
        "output_tokens": 12,
        "reasoning_tokens": 3,
    },
}
prewarm = {
    "sequence": 1,
    "thread_id": "thread-mekugi",
    "provider_attempts": [{"model": "model"}],
    "usage": {
        "input_tokens": 10,
        "cached_input_tokens": 0,
        "output_tokens": 0,
        "reasoning_tokens": 0,
    },
}
metrics = {"exchanges": [prewarm, ordinary]}
results = Path(sys.argv[3])
validate_results(metrics, results, "mekugi", {}, {1})
try:
    validate_results(metrics, results, "mekugi", {})
except ValueError:
    pass
else:
    raise SystemExit("result validation counted provider prewarm usage as Codex turn usage")
PY
grep -Fq '| Control | 1/1 |' "$fixture/summary.md"
grep -Fq '| Mekugi | 1/1 |' "$fixture/summary.md"
grep -Fq 'input **-20** (-20.00%)' "$fixture/summary.md"
grep -Fq '| Carrier token expansion | 6 |' "$fixture/summary.md"
grep -Fq '| Mekugi | 50 | 30 | 62.50% | 30 | 0 | 0 | 0 | n/a |' "$fixture/summary.md"
grep -Fq '| Mekugi | 25 | 20 | 22 | 30 | 17 | 25 |' "$fixture/summary.md"
grep -Fq 'Delivery token expansion' "$fixture/summary.md"
grep -Fq '| Mekugi | 2 | 1 | 0 | 0 | 0 | 0 | 0 |' "$fixture/summary.md"
grep -Fq 'in-process `capturer` on each router listener' "$fixture/summary.md"
if grep -Fq 'thread-mekugi' "$fixture/summary.md"; then
	printf 'report leaked a thread identifier\n' >&2
	exit 1
fi

control_only="$fixture/control-only"
mkdir -p "$control_only/captures"
printf '%s\n' '{"benchmark_mode":"control-only"}' >"$control_only/benchmark-config.json"
grep '"arm":"control"' "$fixture/results.jsonl" >"$control_only/results.jsonl"
cp "$fixture/control-metrics.json" "$control_only/"
cp "$fixture/captures/control.jsonl" "$control_only/captures/"
bash "$benchmark_root/report.sh" "$control_only" >/dev/null
grep -Fq '| Stock | 1/1 |' "$control_only/summary.md"
if grep -Eq 'HPATCH delivery|Actual provider-token change' "$control_only/summary.md"; then
    printf 'control-only report invented a treatment or comparison\n' >&2
    exit 1
fi
jq '.mode = "mekugi"' "$fixture/control-metrics.json" >"$control_only/control-metrics.json"
if bash "$benchmark_root/report.sh" "$control_only" >/dev/null 2>&1; then
    printf 'control-only report accepted a Mekugi control\n' >&2
    exit 1
fi

for single_mode in mekugi-only mekugi-diagnostic; do
	single="$fixture/$single_mode"
	mkdir -p "$single/captures"
	printf '{"benchmark_mode":"%s"}\n' "$single_mode" >"$single/benchmark-config.json"
	grep '"arm":"mekugi"' "$fixture/results.jsonl" >"$single/results.jsonl"
	cp "$fixture/mekugi-metrics.json" "$single/mekugi-metrics.json"
	cp "$fixture/captures/mekugi.jsonl" "$single/captures/mekugi.jsonl"
	bash "$benchmark_root/report.sh" "$single" >/dev/null
	grep -Fq -- "- Mode: \`$single_mode\`" "$single/summary.md"
	grep -Fq '| Mekugi | 1/1 |' "$single/summary.md"
done


# A single CTP treatment retains strict evidence validation without importing a
# differently configured control or scheduling a second model attempt.
single_ctp="$fixture/single-ctp"
mkdir -p "$single_ctp/captures"
printf '%s\n' '{"benchmark_mode":"mekugi-diagnostic","treatment_model_protocol":"ctp2"}' >"$single_ctp/benchmark-config.json"
grep '"arm":"mekugi"' "$fixture/results.jsonl" >"$single_ctp/results.jsonl"
jq '.model_protocol="ctp2"' "$fixture/mekugi-metrics.json" >"$single_ctp/mekugi-metrics.json"
jq -c '.model_protocol="ctp2"' "$fixture/captures/mekugi.jsonl" >"$single_ctp/captures/mekugi.jsonl"
bash "$benchmark_root/report.sh" "$single_ctp" >/dev/null
grep -Fq '| Mekugi + CTP/2 | 1/1 |' "$single_ctp/summary.md"
grep -Fq '### CTP/2 acceptance: Mekugi + CTP/2' "$single_ctp/summary.md"
if grep -Fq 'Actual provider-token change' "$single_ctp/summary.md"; then
 printf 'single CTP report invented a comparison\n' >&2; exit 1
fi
printf '%s\n' '{"benchmark_mode":"mekugi-diagnostic","treatment_model_protocol":"native"}' >"$single_ctp/benchmark-config.json"
if bash "$benchmark_root/report.sh" "$single_ctp" >/dev/null 2>&1; then
 printf 'diagnostic report accepted mismatched protocol\n' >&2; exit 1
fi


provider_evidence="$fixture/provider-evidence"
mkdir -p "$provider_evidence/captures"
printf '%s\n' '{"benchmark_mode":"mekugi-diagnostic"}' >"$provider_evidence/benchmark-config.json"
grep '"arm":"mekugi"' "$fixture/results.jsonl" >"$provider_evidence/results.jsonl"
for state in present missing null invalid unavailable; do
    evidence=$(jq -nc --arg state "$state" '{request_id:"req-private-lookup",model:"response-model",header_model:"header-model",cached_tokens_state:$state} + (if $state == "present" then {cached_tokens:50} else {} end)')
    jq --argjson e "$evidence" '.exchanges[0].provider_attempts[0].provider_response=$e' "$fixture/mekugi-metrics.json" >"$provider_evidence/mekugi-metrics.json"
    jq -c --argjson e "$evidence" 'if .boundary == "provider" then .provider_response=$e else . end' "$fixture/captures/mekugi.jsonl" >"$provider_evidence/captures/mekugi.jsonl"
    bash "$benchmark_root/report.sh" "$provider_evidence" >/dev/null
    count=unavailable
    [[ $state != present ]] || count=50
    grep -Fq "| Mekugi | 1 | 1 | response-model | header-model | $state | $count |" "$provider_evidence/summary.md"
    if grep -Fq 'req-private-lookup' "$provider_evidence/summary.md"; then
        printf 'report leaked provider request ID\n' >&2; exit 1
    fi
done
jq '.exchanges[0].provider_attempts[0].provider_response.model="tampered-model"' "$provider_evidence/mekugi-metrics.json" >"$provider_evidence/tampered.json"
cp "$provider_evidence/tampered.json" "$provider_evidence/mekugi-metrics.json"
if bash "$benchmark_root/report.sh" "$provider_evidence" >/dev/null 2>&1; then
    printf 'report accepted tampered provider evidence\n' >&2; exit 1
fi

ctp="$fixture/ctp"
mkdir -p "$ctp/captures"
printf '%s\n' '{"benchmark_mode":"ctp-only","ctp":{"require_input_compression":true,"require_output_compression":true}}' >"$ctp/benchmark-config.json"
sed -e 's/"arm":"control"/"arm":"native"/' -e 's/"arm":"mekugi"/"arm":"ctp"/' \
	"$fixture/results.jsonl" >"$ctp/results.jsonl"
jq '.mode = "mekugi"' "$fixture/control-metrics.json" >"$ctp/control-metrics.json"
sed 's/"mode":"passthrough"/"mode":"mekugi"/g' "$fixture/captures/control.jsonl" >"$ctp/captures/control.jsonl"
jq '.model_protocol = "ctp2" | .protocol.input_payload_tokens_saved = 5 | .protocol.input_payload_bytes_saved = 20 | .protocol.output_text_tokens_saved = 8 | .exchanges[0].provider_attempts[0].native_request = {bytes:100,tokens:25} | .exchanges[0].client_final_text = {bytes:90,tokens:25}' "$fixture/mekugi-metrics.json" >"$ctp/mekugi-metrics.json"
jq -c '.model_protocol = "ctp2" | if .boundary == "provider" then .native_request = {bytes:100,tokens:25} else .final_text = {bytes:90,tokens:25} end' "$fixture/captures/mekugi.jsonl" >"$ctp/captures/mekugi.jsonl"
bash "$benchmark_root/report.sh" "$ctp" >/dev/null
grep -Fq '| Native protocol | 1/1 |' "$ctp/summary.md"
grep -Fq '| CTP/2 | 1/1 |' "$ctp/summary.md"
grep -Fq '| Input | true | 5 | passed |' "$ctp/summary.md"
grep -Fq '| Output | true | 8 | passed |' "$ctp/summary.md"

paired_ctp="$fixture/paired-ctp"
mkdir -p "$paired_ctp/captures"
printf '%s\n' '{"benchmark_mode":"paired","treatment_model_protocol":"ctp2","ctp":{"require_input_compression":true,"require_output_compression":true}}' >"$paired_ctp/benchmark-config.json"
cp "$fixture/results.jsonl" "$fixture/control-metrics.json" "$paired_ctp/"
cp "$fixture/captures/control.jsonl" "$paired_ctp/captures/"
cp "$ctp/mekugi-metrics.json" "$paired_ctp/"
cp "$ctp/captures/mekugi.jsonl" "$paired_ctp/captures/"
bash "$benchmark_root/report.sh" "$paired_ctp" >/dev/null
grep -Fq '| Stock | 1/1 |' "$paired_ctp/summary.md"
grep -Fq '| Mekugi + CTP/2 | 1/1 |' "$paired_ctp/summary.md"
grep -Fq '| Output | true | 8 | passed |' "$paired_ctp/summary.md"
cp "$fixture/mekugi-metrics.json" "$paired_ctp/"
cp "$fixture/captures/mekugi.jsonl" "$paired_ctp/captures/"
if bash "$benchmark_root/report.sh" "$paired_ctp" >/dev/null 2>&1; then
    printf 'report accepted native treatment for CTP/2 paired preset\n' >&2
    exit 1
fi

ctp_failed="$fixture/ctp-failed"
mkdir -p "$ctp_failed/captures"
cp "$ctp/benchmark-config.json" "$ctp/results.jsonl" "$ctp/control-metrics.json" "$ctp_failed/"
cp "$ctp/captures/control.jsonl" "$ctp_failed/captures/control.jsonl"
jq -c 'if .boundary == "codex" then .final_output = {bytes: 70, tokens: 17} | .final_text = {bytes: 70, tokens: 17} else . end' \
	"$ctp/captures/mekugi.jsonl" >"$ctp_failed/captures/mekugi.jsonl"
jq '.protocol.output_text_tokens_saved = 0 |
 .protocol.output_payload_tokens_expansion = 0 |
	.protocol.output_payload_bytes_expansion = 0 |
	.semantic.client_outputs = {bytes: 70, tokens: 17} |
	.exchanges[0].client_final_text = {bytes: 70, tokens: 17} |
 .exchanges[0].client_final_output = {bytes: 70, tokens: 17}' \
	"$ctp/mekugi-metrics.json" >"$ctp_failed/mekugi-metrics.json"
if bash "$benchmark_root/report.sh" "$ctp_failed" >/dev/null 2>&1; then
	printf 'report accepted missing required CTP output compression\n' >&2
	exit 1
fi
grep -Fq '| Output | true | 0 | failed |' "$ctp_failed/summary.md"

mentor="$fixture/mentor"
mkdir -p "$mentor/captures"
printf '%s\n' '{"benchmark_mode":"mentor-handoff","mentor_handoff":{"mentor_model":"model"}}' >"$mentor/benchmark-config.json"
sed -e '1s/"arm":"control"/"arm":"mekugi"/' -e '2s/"arm":"mekugi"/"arm":"mekugi-mentor"/' \
	"$fixture/results.jsonl" | jq -c --arg proof "$mentor/child-proof.json" '
		if .arm == "mekugi-mentor" then
			.parent_model = .model |
			.parent_reasoning_effort = .reasoning_effort |
			.child_model = .model |
			.child_reasoning_effort = .reasoning_effort |
			.agent.child_proof_path = $proof
		else . end
	' >"$mentor/results.jsonl"
cat >"$mentor/child-proof.json" <<'JSON'
{"schema":"mekugi.benchmark.child-proof.v1","child_thread_id":"thread-child","configured_model":"model","configured_reasoning_effort":"high"}
JSON
jq '.mode = "mekugi"' "$fixture/control-metrics.json" >"$mentor/mekugi-metrics.json"
sed 's/"mode":"passthrough"/"mode":"mekugi"/g' "$fixture/captures/control.jsonl" >"$mentor/captures/control.jsonl"
cp "$fixture/captures/mekugi.jsonl" "$mentor/captures/mekugi.jsonl"
cat >>"$mentor/captures/mekugi.jsonl" <<'JSONL'
{"schema_version":6,"boundary":"provider","capture_id":"mentor-child","request_sequence":2,"provider_attempt":1,"mode":"mekugi","model_protocol":"native","thread_id":"thread-child","request_model":"model","request":{"bytes":0,"tokens":0},"native_request":{"bytes":0,"tokens":0},"status_code":200,"response_complete":true,"response_status":"completed","usage":{"input_tokens":0,"cached_input_tokens":0,"output_tokens":0,"reasoning_tokens":0},"response":{"bytes":0,"tokens":0},"duration_ms":1,"captured_at":"2026-08-28T00:00:00Z"}
{"schema_version":6,"boundary":"codex","capture_id":"mentor-child","request_sequence":2,"mode":"mekugi","model_protocol":"native","thread_id":"thread-child","request_model":"model","request":{"bytes":0,"tokens":0},"native_request":{"bytes":0,"tokens":0},"status_code":200,"response_complete":true,"response_status":"completed","response":{"bytes":0,"tokens":0},"duration_ms":1,"captured_at":"2026-08-28T00:00:00Z"}
JSONL
jq '.requests.logical += 1 |
	.requests.provider_attempts += 1 |
	.requests.completed += 1 |
	.usage.provider_attempts += 1 |
	.capture.records += 2 |
	.exchanges += [{
		sequence: 2, thread_id: "thread-child", model: "model",
		provider_attempts: [{attempt: 1, model: "model", status: "completed", response_complete: true,
			usage: {input_tokens: 0, cached_input_tokens: 0, uncached_input_tokens: 0, output_tokens: 0, reasoning_tokens: 0, provider_attempts: 1},
			request: {bytes: 0, tokens: 0}, native_request: {bytes: 0, tokens: 0}, response: {bytes: 0, tokens: 0}}],
		status: "completed",
		usage: {input_tokens: 0, cached_input_tokens: 0, uncached_input_tokens: 0, output_tokens: 0, reasoning_tokens: 0, provider_attempts: 1},
		client_request: {bytes: 0, tokens: 0}, client_response: {bytes: 0, tokens: 0}
	}]' "$fixture/mekugi-metrics.json" >"$mentor/mekugi-mentor-metrics.json"
bash "$benchmark_root/report.sh" "$mentor" >/dev/null
grep -Fq '| Mekugi | 1/1 |' "$mentor/summary.md"
grep -Fq '| Mekugi + Mentor Handoff | 1/1 |' "$mentor/summary.md"

# Distinct main, mentor, and child models with two independent repetitions per arm.
mentor_ctp="$fixture/mentor-ctp"
python3 - "$mentor" "$mentor_ctp" <<'PY'
import copy
import json
from pathlib import Path
import sys

source, target = map(Path, sys.argv[1:])
(target / "captures").mkdir(parents=True)
config = {
    "benchmark_mode": "mentor-handoff",
    "mentor_handoff": {
        "parent_model": "gpt-6-astra", "mentor_model": "gpt-5.6-sol",
        "child_model": "gpt-5.6-luna", "model_protocol": "ctp2",
    },
    "ctp": {"require_input_compression": True, "require_output_compression": True},
}
(target / "benchmark-config.json").write_text(json.dumps(config))
base_metrics = json.loads((source / "mekugi-mentor-metrics.json").read_text())
base_records = [json.loads(line) for line in (source / "captures/mekugi.jsonl").read_text().splitlines()]
base_result = json.loads((source / "results.jsonl").read_text().splitlines()[1])
base_metrics["protocol"].update(input_payload_tokens_saved=5, input_payload_bytes_saved=20, output_text_tokens_saved=8)
base_metrics["exchanges"][0]["provider_attempts"][0]["native_request"] = {"bytes":100,"tokens":25}
base_metrics["exchanges"][0]["client_final_text"] = {"bytes":90,"tokens":25}
for record in base_records:
    if record["request_sequence"] != 1:
        continue
    if record["boundary"] == "provider":
        record["native_request"] = {"bytes":100,"tokens":25}
    else:
        record["final_text"] = {"bytes":90,"tokens":25}


def twice(value):
    if isinstance(value, dict):
        return {key: twice(item) for key, item in value.items()}
    return value * 2 if type(value) is int else value

results = []
for arm, capture_name, child_provider in (
    ("mekugi", "control", "gpt-5.6-luna"),
    ("mekugi-mentor", "mekugi", "gpt-5.6-sol"),
):
    metrics = twice(base_metrics)
    metrics["model_protocol"] = "ctp2"
    metrics["exchanges"] = []
    records = []
    for repetition in (1, 2):
        root = f"{arm}-root-{repetition}"
        child = f"{arm}-child-{repetition}"
        for original in base_metrics["exchanges"]:
            exchange = copy.deepcopy(original)
            is_child = exchange["thread_id"] == "thread-child"
            exchange["thread_id"] = child if is_child else root
            exchange["sequence"] += (repetition - 1) * 2
            exchange["model"] = child_provider if is_child else "gpt-6-astra"
            for attempt in exchange["provider_attempts"]:
                attempt["model"] = exchange["model"]
            metrics["exchanges"].append(exchange)
        for original in base_records:
            record = copy.deepcopy(original)
            is_child = record["thread_id"] == "thread-child"
            record["thread_id"] = child if is_child else root
            record["request_model"] = child_provider if is_child else "gpt-6-astra"
            record["request_sequence"] += (repetition - 1) * 2
            record["capture_id"] += f"-{arm}-{repetition}"
            record["model_protocol"] = "ctp2"
            records.append(record)
        proof = f"{arm}-proof-{repetition}.json"
        (target / proof).write_text(json.dumps({
            "schema": "mekugi.benchmark.child-proof.v1", "child_thread_id": child,
            "configured_model": "gpt-5.6-luna", "configured_reasoning_effort": "high",
        }))
        result = copy.deepcopy(base_result)
        result.update(arm=arm, repetition=repetition, model="gpt-5.6-luna",
                      parent_model="gpt-6-astra", child_model="gpt-5.6-luna",
                      model_protocol="ctp2", router_mode="mekugi")
        result["agent"].update(thread_id=root, child_proof_path=proof)
        results.append(result)
    (target / f"{arm}-metrics.json").write_text(json.dumps(metrics))
    (target / f"captures/{capture_name}.jsonl").write_text("".join(json.dumps(row) + "\n" for row in records))
(target / "results.jsonl").write_text("".join(json.dumps(row) + "\n" for row in results))
PY
bash "$benchmark_root/report.sh" "$mentor_ctp" >/dev/null
grep -Fq -- '- Model: `gpt-6-astra`' "$mentor_ctp/summary.md"
grep -Fq -- '- Both arms model protocol: `ctp2`' "$mentor_ctp/summary.md"
grep -Fq '| Mekugi | 2/2 |' "$mentor_ctp/summary.md"
grep -Fq '| Mekugi + Mentor Handoff | 2/2 |' "$mentor_ctp/summary.md"
grep -Fq '### CTP/2 acceptance: Mekugi + Mentor Handoff' "$mentor_ctp/summary.md"
[[ $(grep -Fc '| Output | true | 16 | passed |' "$mentor_ctp/summary.md") == 2 ]]
if grep -Eq 'mekugi-root-|mekugi-mentor-child-' "$mentor_ctp/summary.md"; then
	printf 'Mentor CTP report leaked a thread identifier\n' >&2
	exit 1
fi

for failure in baseline-compression wrong-protocol wrong-parent missing-mentor; do
	broken="$fixture/mentor-ctp-$failure"
	cp -R "$mentor_ctp" "$broken"
	python3 - "$broken" "$failure" <<'PY'
import json
from pathlib import Path
import sys

root = Path(sys.argv[1])
failure = sys.argv[2]
baseline = failure == "baseline-compression"
metrics_path = root / ("mekugi-metrics.json" if baseline else "mekugi-mentor-metrics.json")
capture_path = root / ("captures/control.jsonl" if baseline else "captures/mekugi.jsonl")
metrics = json.loads(metrics_path.read_text())
records = [json.loads(line) for line in capture_path.read_text().splitlines()]
if baseline:
    metrics["protocol"]["output_text_tokens_saved"] = 0
    metrics["protocol"]["output_payload_tokens_expansion"] = 0
    metrics["protocol"]["output_payload_bytes_expansion"] = 0
    metrics["semantic"]["client_outputs"] = metrics["semantic"]["provider_attempt_outputs"]
    for exchange in metrics["exchanges"]:
        exchange["client_final_output"] = exchange["provider_attempts"][0].get("final_output")
        exchange["client_final_text"] = exchange["provider_attempts"][0].get("final_text")
    for record in records:
        if record["boundary"] == "codex" and record.get("final_output"):
            record["final_output"] = {"bytes": 70, "tokens": 17}
            record["final_text"] = {"bytes": 70, "tokens": 17}
elif failure == "wrong-protocol":
    metrics["model_protocol"] = "native"
    for record in records:
        record["model_protocol"] = "native"
else:
    marker = "-root-" if failure == "wrong-parent" else "-child-"
    for exchange in metrics["exchanges"]:
        if marker in exchange["thread_id"]:
            exchange["model"] = "gpt-5.6-luna"
            exchange["provider_attempts"][0]["model"] = "gpt-5.6-luna"
    for record in records:
        if marker in record["thread_id"]:
            record["request_model"] = "gpt-5.6-luna"
metrics_path.write_text(json.dumps(metrics))
capture_path.write_text("".join(json.dumps(row) + "\n" for row in records))
PY
	if bash "$benchmark_root/report.sh" "$broken" >/dev/null 2>&1; then
		printf 'Mentor CTP report accepted %s\n' "$failure" >&2
		exit 1
	fi
	if [[ $failure == baseline-compression ]]; then
		grep -Fq '| Output | true | 0 | failed |' "$broken/summary.md"
	fi
done

no_usage="$fixture/no-usage-retry"
mkdir -p "$no_usage/captures"
printf '%s\n' '{"benchmark_mode":"mekugi-only"}' >"$no_usage/benchmark-config.json"
grep '"arm":"mekugi"' "$fixture/results.jsonl" >"$no_usage/results.jsonl"
jq '.requests.provider_attempts += 1 |
	.capture.records += 1 |
	.transport.provider_attempt_requests.bytes += 80 |
	.transport.provider_attempt_requests.tokens += 20 |
	.transport.provider_responses.bytes += 20 |
	.transport.provider_responses.tokens += 5 |
	.exchanges[0].provider_attempts[0].attempt = 2 |
	.exchanges[0].provider_attempts = [{attempt: 1, model: "model", status: "http_error", response_complete: true,
		request: {bytes: 80, tokens: 20}, native_request: {bytes: 80, tokens: 20}, response: {bytes: 20, tokens: 5}}] + .exchanges[0].provider_attempts' \
	"$fixture/mekugi-metrics.json" >"$no_usage/mekugi-metrics.json"
{
	printf '%s\n' '{"schema_version":6,"boundary":"provider","capture_id":"mekugi-id","request_sequence":1,"provider_attempt":1,"mode":"mekugi","model_protocol":"native","thread_id":"thread-mekugi","request_model":"model","request":{"bytes":80,"tokens":20},"native_request":{"bytes":80,"tokens":20},"status_code":429,"response_complete":true,"response_status":"http_error","response":{"bytes":20,"tokens":5},"duration_ms":1,"captured_at":"2026-08-28T00:00:00Z"}'
	sed 's/"provider_attempt":1/"provider_attempt":2/' "$fixture/captures/mekugi.jsonl"
} >"$no_usage/captures/mekugi.jsonl"
bash "$benchmark_root/report.sh" "$no_usage" >/dev/null
grep -Fq '| Mekugi | `model` | 2 | 1 | 80 | 50 | 12 | 3 |' "$no_usage/summary.md"

jq '.capture.incomplete_records = 1' "$fixture/mekugi-metrics.json" >"$fixture/bad-metrics.json"
if python3 "$benchmark_root/analyze_capture.py" "$fixture/bad-metrics.json" \
	"$fixture/captures/mekugi.jsonl" "$fixture/results.jsonl" mekugi >/dev/null 2>&1; then
	printf 'capture validator accepted incomplete evidence\n' >&2
	exit 1
fi

jq '.usage.output_tokens = 13' "$fixture/mekugi-metrics.json" >"$fixture/bad-usage.json"
if python3 "$benchmark_root/analyze_capture.py" "$fixture/bad-usage.json" \
	"$fixture/captures/mekugi.jsonl" "$fixture/results.jsonl" mekugi >/dev/null 2>&1; then
	printf 'capture validator accepted unreconciled usage\n' >&2
	exit 1
fi

derived_usage_mutations=(
	'.usage.uncached_input_tokens += 1'
	'.usage.provider_attempts = 0'
	'.exchanges[0].usage.uncached_input_tokens += 1'
	'.exchanges[0].usage.provider_attempts = 2'
)
for index in "${!derived_usage_mutations[@]}"; do
	bad="$fixture/bad-derived-usage-$index.json"
	jq "${derived_usage_mutations[$index]}" "$fixture/mekugi-metrics.json" >"$bad"
	if python3 "$benchmark_root/analyze_capture.py" "$bad" \
		"$fixture/captures/mekugi.jsonl" "$fixture/results.jsonl" mekugi >/dev/null 2>&1; then
		printf 'capture validator accepted unreconciled derived usage: %s\n' \
			"${derived_usage_mutations[$index]}" >&2
		exit 1
	fi
done

derived_metric_mutations=(
	'.cache.provider_cache_rate = 0.99'
	'.semantic.client_outputs.tokens += 1'
	'.protocol.output_payload_tokens_expansion += 1'
	'.mekugi.carrier_input_tokens_expansion += 1'
)
for index in "${!derived_metric_mutations[@]}"; do
	bad="$fixture/bad-derived-metric-$index.json"
	jq "${derived_metric_mutations[$index]}" "$fixture/mekugi-metrics.json" >"$bad"
	if python3 "$benchmark_root/analyze_capture.py" "$bad" \
		"$fixture/captures/mekugi.jsonl" "$fixture/results.jsonl" mekugi >/dev/null 2>&1; then
		printf 'capture validator accepted an unreconciled derived metric: %s\n' \
			"${derived_metric_mutations[$index]}" >&2
		exit 1
	fi
done

duplicate_sequence="$fixture/duplicate-raw-sequence"
mkdir -p "$duplicate_sequence"
jq '.capture.records = 4 | .exchanges += [(.exchanges[0] | .sequence = 2)]' \
	"$fixture/mekugi-metrics.json" >"$duplicate_sequence/metrics.json"
{
	cat "$fixture/captures/mekugi.jsonl"
	sed 's/"capture_id":"mekugi-id"/"capture_id":"duplicate-id"/g' \
		"$fixture/captures/mekugi.jsonl"
} >"$duplicate_sequence/capture.jsonl"
if PYTHONPATH="$benchmark_root" python3 - "$duplicate_sequence/metrics.json" \
	"$duplicate_sequence/capture.jsonl" >/dev/null 2>&1 <<'PY'
from pathlib import Path
import sys
from analyze_capture import load_json, validate_raw_capture

validate_raw_capture(Path(sys.argv[2]), load_json(Path(sys.argv[1])))
PY
then
	printf 'capture validator accepted duplicate raw request sequences\n' >&2
	exit 1
fi

jq '.exchanges[0].provider_attempts[0].model = "wrong-model"' "$fixture/mekugi-metrics.json" >"$fixture/bad-model.json"
sed 's/"request_model":"model"/"request_model":"wrong-model"/g' "$fixture/captures/mekugi.jsonl" >"$fixture/bad-model-capture.jsonl"
if python3 "$benchmark_root/analyze_capture.py" "$fixture/bad-model.json" \
	"$fixture/bad-model-capture.jsonl" "$fixture/results.jsonl" mekugi >/dev/null 2>&1; then
	printf 'capture validator accepted the wrong provider model\n' >&2
	exit 1
fi

jq '.mode = "passthrough"' "$fixture/mekugi-metrics.json" >"$fixture/bad-mode.json"
sed 's/"mode":"mekugi"/"mode":"passthrough"/g' "$fixture/captures/mekugi.jsonl" >"$fixture/bad-mode-capture.jsonl"
if python3 "$benchmark_root/analyze_capture.py" "$fixture/bad-mode.json" \
	"$fixture/bad-mode-capture.jsonl" "$fixture/results.jsonl" mekugi >/dev/null 2>&1; then
	printf 'capture validator accepted the wrong treatment mode\n' >&2
	exit 1
fi

for missing in control-metrics.json captures/control.jsonl; do
	broken="$fixture/missing-baseline-${missing//\//-}"
	mkdir -p "$broken/captures"
	cp "$fixture/benchmark-config.json" "$fixture/results.jsonl" "$fixture/control-metrics.json" "$fixture/mekugi-metrics.json" "$broken/"
	cp "$fixture/captures/control.jsonl" "$fixture/captures/mekugi.jsonl" "$broken/captures/"
	rm -f "$broken/$missing"
	if bash "$benchmark_root/report.sh" "$broken" >/dev/null 2>&1; then
		printf 'report accepted a missing baseline artifact: %s\n' "$missing" >&2
		exit 1
	fi
done

broken="$fixture/wrong-baseline-schema"
mkdir -p "$broken/captures"
cp "$fixture/benchmark-config.json" "$fixture/results.jsonl" "$fixture/control-metrics.json" "$fixture/mekugi-metrics.json" "$broken/"
cp "$fixture/captures/control.jsonl" "$fixture/captures/mekugi.jsonl" "$broken/captures/"
jq '.schema = "wrong"' "$broken/control-metrics.json" >"$broken/control-metrics.tmp"
mv "$broken/control-metrics.tmp" "$broken/control-metrics.json"
if bash "$benchmark_root/report.sh" "$broken" >/dev/null 2>&1; then
	printf 'report accepted a wrong baseline schema\n' >&2
	exit 1
fi

# Old accounting cannot be relabeled as corrected evidence: raw payloads were
# not retained, so the missing semantic output cannot be reconstructed offline.
jq '.schema = "mekugi.capture.metrics.v3"' "$fixture/mekugi-metrics.json" >"$fixture/old-metrics.json"
if python3 "$benchmark_root/analyze_capture.py" "$fixture/old-metrics.json" \
    "$fixture/captures/mekugi.jsonl" "$fixture/results.jsonl" mekugi >/dev/null 2>&1; then
    printf 'validator accepted old output accounting\n' >&2; exit 1
fi
sed 's/"schema_version":6/"schema_version":5/g' "$fixture/captures/mekugi.jsonl" >"$fixture/old-capture.jsonl"
if python3 "$benchmark_root/analyze_capture.py" "$fixture/mekugi-metrics.json" \
    "$fixture/old-capture.jsonl" "$fixture/results.jsonl" mekugi >/dev/null 2>&1; then
    printf 'validator accepted old capture output accounting\n' >&2; exit 1
fi

jq '.exchanges[0].provider_attempts[0].transport = "websocket" |
    .exchanges[0].provider_attempts[0].native_request.tokens += 1 |
    .protocol.input_payload_tokens_saved += 1' \
    "$fixture/mekugi-metrics.json" >"$fixture/websocket-native.json"
jq -c 'if .boundary == "provider" then
    .transport = "websocket" | .native_request.tokens += 1
else . end' \
    "$fixture/captures/mekugi.jsonl" >"$fixture/websocket-native-capture.jsonl"
python3 "$benchmark_root/analyze_capture.py" \
    "$fixture/websocket-native.json" "$fixture/websocket-native-capture.jsonl" \
    "$fixture/results.jsonl" mekugi >/dev/null
jq '.exchanges[0].provider_attempts[0].native_request.tokens += 1 | .protocol.input_payload_tokens_saved += 1' "$fixture/mekugi-metrics.json" >"$fixture/false-native.json"
jq -c 'if .boundary == "provider" then .native_request.tokens += 1 else . end' "$fixture/captures/mekugi.jsonl" >"$fixture/false-native-capture.jsonl"
if python3 "$benchmark_root/analyze_capture.py" "$fixture/false-native.json" "$fixture/false-native-capture.jsonl" "$fixture/results.jsonl" mekugi >/dev/null 2>&1; then
    printf 'validator accepted false native compression\n' >&2; exit 1
fi
jq 'del(.exchanges[0].provider_attempts[0].native_request)' "$no_usage/mekugi-metrics.json" >"$fixture/missing-retry-baseline.json"
jq -c 'if .boundary == "provider" and .provider_attempt == 1 then del(.native_request) else . end' "$no_usage/captures/mekugi.jsonl" >"$fixture/missing-retry-baseline-capture.jsonl"
if python3 "$benchmark_root/analyze_capture.py" "$fixture/missing-retry-baseline.json" "$fixture/missing-retry-baseline-capture.jsonl" "$no_usage/results.jsonl" mekugi >/dev/null 2>&1; then
    printf 'validator accepted missing retry baseline\n' >&2; exit 1
fi
printf 'report tests passed\n'
