#!/usr/bin/env bash
set -euo pipefail

benchmark_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
fixture=$(mktemp -d)
trap 'rm -rf -- "$fixture"' EXIT
mkdir -p "$fixture/captures"

python3 - "$fixture" <<'PY'
from pathlib import Path
import json, sys

root = Path(sys.argv[1])
(root / "benchmark-config.json").write_text('{"benchmark_mode":"paired"}\n')
results = []
zero = {"bytes": 0, "tokens": 0}
for arm, mode, input_tokens, cached, output_tokens, reasoning in (
    ("control", "passthrough", 100, 40, 20, 5),
    ("mekugi", "mekugi", 80, 50, 12, 3),
):
    thread = "thread-" + arm
    capture_id = arm + "-capture"
    client_request = {"bytes": 100, "tokens": 25}
    provider_request = {"bytes": 80, "tokens": 20}
    client_response = {"bytes": 120, "tokens": 30}
    provider_response = {"bytes": 90, "tokens": 22}
    client_output = {"bytes": 90, "tokens": 25}
    provider_output = {"bytes": 70, "tokens": 17}
    usage = {"input_tokens": input_tokens, "cached_input_tokens": cached,
             "output_tokens": output_tokens, "reasoning_tokens": reasoning}
    measured_usage = {**usage, "uncached_input_tokens": input_tokens - cached,
                      "provider_attempts": 1}
    provider = {
        "schema_version": 7, "boundary": "provider", "capture_id": capture_id,
        "request_sequence": 1, "provider_attempt": 1, "mode": mode,
        "thread_id": thread, "request_model": "model", "request": provider_request,
        "projected_request": provider_request, "status_code": 200,
        "response_complete": True, "response_status": "completed", "usage": usage,
        "response": provider_response, "final_output": provider_output,
        "final_text": provider_output, "duration_ms": 10,
        "captured_at": "2026-09-15T10:00:00Z",
    }
    client = {
        "schema_version": 7, "boundary": "codex", "capture_id": capture_id,
        "request_sequence": 1, "mode": mode, "thread_id": thread,
        "request": client_request, "status_code": 200, "response_complete": True,
        "response_status": "completed", "response": client_response,
        "final_output": client_output, "final_text": provider_output,
        "duration_ms": 12, "captured_at": "2026-09-15T10:00:00Z",
    }
    (root / "captures" / f"{arm}.jsonl").write_text(
        json.dumps(provider, separators=(",", ":")) + "\n" +
        json.dumps(client, separators=(",", ":")) + "\n"
    )
    metrics = {
        "schema": "mekugi.capture.metrics.v6", "mode": mode,
        "requests": {"logical": 1, "provider_attempts": 1, "completed": 1, "failed": 0},
        "usage": measured_usage,
        "cache": {
            "attribution_basis": "previous_input_length_estimate",
            "cold_or_new_uncached_input_tokens": input_tokens - cached,
            "provider_cache_rate": cached / input_tokens,
            "eligible_prefix_tokens": 0, "eligible_prefix_cached_tokens": 0,
            "eligible_prefix_miss_tokens": 0, "eligible_prefix_cache_rate": None,
        },
        "transport": {
            "client_control_requests": zero, "client_requests": client_request,
            "provider_attempt_requests": provider_request, "provider_control_requests": zero,
            "provider_control_responses": zero, "provider_responses": provider_response,
            "client_control_responses": zero, "client_responses": client_response,
        },
        "semantic": {"provider_attempt_outputs": provider_output, "client_outputs": client_output},
        "provider_tools": {}, "delivered_tools": {},
        "exchanges": [{
            "sequence": 1, "thread_id": thread, "model": "model",
            "provider_attempts": [{
                "attempt": 1, "model": "model", "status": "completed", "response_complete": True,
                "usage": measured_usage, "request": provider_request,
                "projected_request": provider_request, "response": provider_response,
                "final_output": provider_output, "final_text": provider_output,
            }],
            "status": "completed", "usage": measured_usage,
            "client_request": client_request, "client_response": client_response,
            "client_final_output": client_output, "client_final_text": provider_output,
        }],
        "capture": {
            "records": 2, "capture_errors": 0, "incomplete_records": 0,
            "missing_provider_records": 0, "provider_attempt_gaps": 0,
            "write_errors": 0, "skipped_requests": 0, "dropped_exchange_details": 0,
        },
    }
    (root / f"{arm}-metrics.json").write_text(json.dumps(metrics) + "\n")
    results.append({
        "task_id": "fixture", "arm": arm, "model": "model", "reasoning_effort": "high",
        "task_pass": True,
        "agent": {"thread_id": thread, "duration_ms": 1000,
                  "usage": {"input_tokens": input_tokens, "cached_input_tokens": cached,
                            "output_tokens": output_tokens, "reasoning_output_tokens": reasoning}},
    })
(root / "results.jsonl").write_text("".join(json.dumps(item) + "\n" for item in results))
PY

bash "$benchmark_root/report.sh" "$fixture" >/dev/null
grep -Fq '| Stock | 1/1 |' "$fixture/summary.md"
grep -Fq '| Mekugi | 1/1 |' "$fixture/summary.md"
grep -Fq 'input **-20** (-20.00%)' "$fixture/summary.md"
grep -Fq '| Mekugi | 50 | 30 | 62.50% | 30 | 0 | 0 | 0 | n/a |' "$fixture/summary.md"
grep -Fq 'Observed payload-token estimates' "$fixture/summary.md"
grep -Fq 'schema-7 JSONL' "$fixture/summary.md"
if grep -Fq 'thread-mekugi' "$fixture/summary.md"; then
    printf 'report leaked a thread identifier\n' >&2
    exit 1
fi

# An observed provider attempt, its snapshot, and Codex result must reconcile.
python3 - "$fixture/captures/mekugi.jsonl" "$fixture/bad-capture.jsonl" <<'PY'
import json, sys
records = [json.loads(line) for line in open(sys.argv[1])]
records[0]["usage"]["input_tokens"] += 1
with open(sys.argv[2], "w") as out:
    for record in records:
        out.write(json.dumps(record) + "\n")
PY
if python3 "$benchmark_root/analyze_capture.py" "$fixture/mekugi-metrics.json" \
    "$fixture/bad-capture.jsonl" "$fixture/results.jsonl" mekugi >/dev/null 2>&1; then
    printf 'capture validator accepted a provider-usage mismatch\n' >&2
    exit 1
fi
jq '.capture.incomplete_records = 1' "$fixture/mekugi-metrics.json" >"$fixture/bad-health.json"
if python3 "$benchmark_root/analyze_capture.py" "$fixture/bad-health.json" \
    "$fixture/captures/mekugi.jsonl" "$fixture/results.jsonl" mekugi >/dev/null 2>&1; then
    printf 'capture validator accepted incomplete evidence\n' >&2
    exit 1
fi
jq -c 'if .arm == "mekugi" then .agent.usage.output_tokens += 1 else . end' \
    "$fixture/results.jsonl" >"$fixture/bad-results.jsonl"
if python3 "$benchmark_root/analyze_capture.py" "$fixture/mekugi-metrics.json" \
    "$fixture/captures/mekugi.jsonl" "$fixture/bad-results.jsonl" mekugi >/dev/null 2>&1; then
    printf 'capture validator accepted result-usage mismatch\n' >&2
    exit 1
fi

control_only="$fixture/control-only"
mkdir -p "$control_only/captures"
printf '%s\n' '{"benchmark_mode":"control-only"}' >"$control_only/benchmark-config.json"
grep '"arm": "control"' "$fixture/results.jsonl" >"$control_only/results.jsonl"
cp "$fixture/control-metrics.json" "$control_only/"
cp "$fixture/captures/control.jsonl" "$control_only/captures/"
bash "$benchmark_root/report.sh" "$control_only" >/dev/null
grep -Fq '| Stock | 1/1 |' "$control_only/summary.md"
if grep -Fq 'Actual provider-token change' "$control_only/summary.md"; then
    printf 'control-only report invented a comparison\n' >&2
    exit 1
fi
jq '.mode = "mekugi"' "$fixture/control-metrics.json" >"$control_only/control-metrics.json"
if bash "$benchmark_root/report.sh" "$control_only" >/dev/null 2>&1; then
    printf 'control-only report accepted a mismatched capture mode\n' >&2
    exit 1
fi
