#!/usr/bin/env python3
"""Validate capture evidence against its capturer-owned metrics snapshot."""

from __future__ import annotations

import argparse
import json
import math
import sys
from collections import defaultdict
from pathlib import Path
from typing import Any

from benchmark_jsonl import load_jsonl

USAGE_KEYS = ("input_tokens", "cached_input_tokens", "output_tokens", "reasoning_tokens")
EXPECTED_ARM_CONFIG = {
    "control": "passthrough",
    "mekugi": "mekugi",
    "mekugi-mentor": "mekugi",
}
INFERENCE_TRANSPORT_KEYS = (
    "client_requests",
    "provider_attempt_requests",
    "provider_responses",
    "client_responses",
)
CONTROL_TRANSPORT_FIELDS = {
    ("codex_control", "request"): ("client_control_requests", "request"),
    ("provider_control", "request"): ("provider_control_requests", "request"),
    ("provider_control", "response"): ("provider_control_responses", "response"),
    ("codex_control", "response"): ("client_control_responses", "response"),
}
CONTROL_TRANSPORT_KEYS = tuple(field for field, _ in CONTROL_TRANSPORT_FIELDS.values())
TRANSPORT_KEYS = INFERENCE_TRANSPORT_KEYS + CONTROL_TRANSPORT_KEYS



def load_json(path: Path) -> dict[str, Any]:
    value = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(value, dict):
        raise ValueError(f"{path} must contain one JSON object")
    return value


def usage(value: object, *, result: bool = False) -> dict[str, int]:
    if not isinstance(value, dict):
        raise ValueError("usage must be an object")
    parsed: dict[str, int] = {}
    for key in USAGE_KEYS:
        source = "reasoning_output_tokens" if result and key == "reasoning_tokens" else key
        count = value.get(source)
        if type(count) is not int or count < 0:
            raise ValueError(f"usage {source} must be a non-negative integer")
        parsed[key] = count
    if parsed["cached_input_tokens"] > parsed["input_tokens"]:
        raise ValueError("cached input exceeds total input")
    return parsed


def add(target: dict[str, int], value: dict[str, int]) -> None:
    for key in USAGE_KEYS:
        target[key] += value[key]


FINGERPRINT_FIELDS = ("model", "instructions", "tools", "reasoning", "tool_choice", "parallel_tool_calls", "other")


def validate_fingerprint(value):
    if value is None:
        return
    def digest(text):
        return isinstance(text, str) and len(text) == 32 and all(c in "0123456789abcdef" for c in text)
    if not isinstance(value, dict) or not digest(value.get("scope")):
        raise ValueError("invalid private cache fingerprint")
    fields, items = value.get("fields"), value.get("items")
    count = value.get("item_count")
    if (not isinstance(fields, dict) or "other" not in fields or any(k not in FINGERPRINT_FIELDS or not digest(v) for k, v in fields.items())
        or not isinstance(items, list) or len(items) > 128 or any(not digest(v) for v in items)
        or type(count) is not int or count < 0 or len(items) != min(count, 128) or type(value.get("complete")) is not bool
        or value["complete"] != (count == len(items))
        or value.get("input_kind") not in {"array", "string", "other", "absent"}):
        raise ValueError("invalid private cache fingerprint shape")
    if (value["input_kind"] == "absent" and count != 0) or (value["input_kind"] in {"string", "other"} and count != 1):
        raise ValueError("cache fingerprint item count disagrees with input kind")
    for key in ("request_key", "routing_key"):
        if key in value and not digest(value[key]):
            raise ValueError("invalid private cache routing fingerprint")

    if "turn_state" in value and value["turn_state"] != "" and not digest(value["turn_state"]):
        raise ValueError("invalid private turn-state fingerprint")


def compare_turn_state(client, provider):
    if (client is None or provider is None or client["scope"] != provider["scope"]
        or "turn_state" not in client or "turn_state" not in provider):
        return "unavailable"
    left, right = client["turn_state"], provider["turn_state"]
    if left == "" and right == "":
        return "absent"
    if left == right:
        return "preserved"
    return "dropped" if right == "" else "changed"


def compare_prefix(previous, current):
    result = {"status": "unavailable", "common_items": 0, "changed_fields": []}
    if (previous is None or current is None or not previous["complete"] or not current["complete"]
        or previous["scope"] != current["scope"]):
        return result
    result["changed_fields"] = [k for k in FINGERPRINT_FIELDS if previous["fields"].get(k) != current["fields"].get(k)]
    for left, right in zip(previous["items"], current["items"]):
        if left != right:
            break
        result["common_items"] += 1
    if previous["input_kind"] != current["input_kind"] or result["changed_fields"] or result["common_items"] < len(previous["items"]):
        result["status"] = "changed"
    else:
        result["status"] = "identical" if len(previous["items"]) == len(current["items"]) else "appended"
    return result


def compare_route(previous, current, key):
    if previous is None or current is None or previous["scope"] != current["scope"]:
        return "unavailable"
    left, right = previous.get(key), current.get(key)
    return "absent" if left is None and right is None else "stable" if left == right else "changed"


def validate_cache_diagnostics(exchanges):
    by_sequence = {e["sequence"]: e for e in exchanges}
    for exchange in exchanges:
        thread = exchange.get("thread_id")
        attempts = exchange["provider_attempts"]
        validate_fingerprint(exchange.get("client_fingerprint"))
        for attempt in attempts:
            validate_provider_evidence(attempt.get("provider_response"), attempt.get("usage"))
            validate_fingerprint(attempt.get("cache_fingerprint"))
            validate_fingerprint(attempt.get("projected_fingerprint"))
        client_fp = exchange.get("client_fingerprint")
        provider_fp = attempts[-1].get("cache_fingerprint") if attempts else None
        has_turn_state = "turn_state" in (client_fp or {}) or "turn_state" in (provider_fp or {})
        if (not thread and not has_turn_state) or not attempts or provider_fp is None:
            if exchange.get("cache_diagnostics") is not None:
                raise ValueError("unavailable cache diagnosis presented as measured")
            continue
        final = attempts[-1]
        predecessor = exchange.get("predecessor_sequence", 0)
        if type(predecessor) is not int or predecessor < 0 or predecessor >= exchange["sequence"]:
            raise ValueError("invalid same-thread arrival predecessor")
        before = by_sequence.get(predecessor) if thread else None
        if before and (before.get("thread_id") != thread or before.get("status") != "completed" or not before["provider_attempts"]):
            before = None
        last = before["provider_attempts"][-1] if before else {}
        expected = {
            "previous_sequence": before["sequence"] if before else 0,
            "client": compare_prefix(before.get("client_fingerprint") if before else None, exchange.get("client_fingerprint")),
            "projected": compare_prefix(last.get("projected_fingerprint"), final.get("projected_fingerprint")),
            "provider": compare_prefix(last.get("cache_fingerprint"), final.get("cache_fingerprint")),
            "routing": compare_route(last.get("cache_fingerprint"), final.get("cache_fingerprint"), "routing_key"),
            "request_key": compare_route(last.get("cache_fingerprint"), final.get("cache_fingerprint"), "request_key"),
        }
        if has_turn_state:
            expected["turn_state_forwarding"] = compare_turn_state(client_fp, provider_fp)
        if exchange.get("cache_diagnostics") != expected:
            raise ValueError("cache diagnosis does not reconcile stage fingerprints")


def empty_usage() -> dict[str, int]:
    return {key: 0 for key in USAGE_KEYS}


def payload(value: object) -> dict[str, int]:
    if value is None:
        return {"bytes": 0, "tokens": 0}
    if not isinstance(value, dict):
        raise ValueError("payload metrics must be an object")
    measured: dict[str, int] = {}
    for key in ("bytes", "tokens"):
        count = value.get(key)
        if type(count) is not int or count < 0:
            raise ValueError(f"payload {key} must be a non-negative integer")
        measured[key] = count
    return measured


def add_payload(target: dict[str, int], value: object) -> None:
    measured = payload(value)
    target["bytes"] += measured["bytes"]
    target["tokens"] += measured["tokens"]


def empty_payload() -> dict[str, int]:
    return {"bytes": 0, "tokens": 0}


def add_tool(totals: dict[str, dict[str, int]], call: object) -> None:
    if not isinstance(call, dict) or not isinstance(call.get("name"), str) or not call["name"]:
        raise ValueError("tool metric is missing its name")
    aggregate = totals.setdefault(
        call["name"],
        {"calls": 0, "input_bytes": 0, "input_tokens": 0, "item_bytes": 0, "item_tokens": 0},
    )
    aggregate["calls"] += 1
    for key in ("input_bytes", "input_tokens", "item_bytes", "item_tokens"):
        count = call.get(key)
        if type(count) is not int or count < 0:
            raise ValueError(f"tool metric {key} must be a non-negative integer")
        aggregate[key] += count


def validate_rate(actual: object, numerator: int, denominator: int, description: str) -> None:
    if denominator == 0:
        if actual is not None:
            raise ValueError(f"{description} must be null without a denominator")
        return
    if not isinstance(actual, (int, float)) or isinstance(actual, bool) or not math.isclose(
        float(actual), numerator / denominator, rel_tol=1e-12, abs_tol=1e-12
    ):
        raise ValueError(f"{description} does not reconcile its token totals")



def validate_provider_evidence(value, measured_usage):
    if value is None:
        return
    fields = {"request_id", "header_model", "model", "cached_tokens_state", "cached_tokens"}
    if not isinstance(value, dict) or set(value) - fields:
        raise ValueError("invalid provider response evidence")
    for key in ("request_id", "header_model", "model"):
        if key in value:
            text = value[key]
            if (not isinstance(text, str) or not 1 <= len(text) <= 256
                or any(not (c.isascii() and (c.isalnum() or c in "-_.:/")) for c in text)):
                raise ValueError("unsafe provider response identifier")
    state = value.get("cached_tokens_state")
    if state not in {"unavailable", "missing", "null", "invalid", "present"}:
        raise ValueError("invalid cached-token evidence state")
    if state == "present":
        count = value.get("cached_tokens")
        if type(count) is not int or not 0 <= count < 2**64:
            raise ValueError("invalid explicit provider cached count")
        if measured_usage is not None and count != measured_usage.get("cached_input_tokens"):
            raise ValueError("provider cached count differs from usage")
    elif "cached_tokens" in value:
        raise ValueError("missing provider telemetry represented as a count")


def validate_raw_capture(path: Path, metrics: dict[str, Any]) -> set[int]:
    records = load_jsonl(path)
    if not records:
        raise ValueError("capture is empty")
    groups: dict[str, dict[str, list[dict[str, Any]]]] = defaultdict(
        lambda: {"codex": [], "provider": []}
    )
    control_transport = {key: empty_payload() for key in CONTROL_TRANSPORT_KEYS}
    for record in records:
        boundary = record.get("boundary")
        capture_id = record.get("capture_id")
        if record.get("schema_version") != 7 or boundary not in {
            "codex",
            "provider",
            "codex_control",
            "provider_control",
        }:
            raise ValueError("capture has an unsupported schema or boundary")
        if record.get("mode") != metrics.get("mode"):
            raise ValueError("raw capture mode differs from the metrics snapshot")
        if not isinstance(capture_id, str) or not capture_id:
            raise ValueError("capture record is missing its correlation identity")
        if record.get("capture_error") or record.get("response_complete") is not True:
            raise ValueError("capture contains a failed or incomplete record")
        if boundary in {"codex_control", "provider_control"}:
            transport_field = CONTROL_TRANSPORT_FIELDS.get((boundary, record.get("control_direction")))
            if transport_field is None:
                raise ValueError("capture has an invalid control direction")
            total, payload_field = transport_field
            add_payload(control_transport[total], record.get(payload_field))
            continue
        groups[capture_id][boundary].append(record)

    exchanges = metrics.get("exchanges")
    if not isinstance(exchanges, list):
        raise ValueError("metrics are missing exchanges")
    exchanges_by_sequence: dict[int, dict[str, Any]] = {}
    for exchange in exchanges:
        sequence = exchange.get("sequence") if isinstance(exchange, dict) else None
        if type(sequence) is not int or sequence < 1 or sequence in exchanges_by_sequence:
            raise ValueError("metrics have a missing or duplicate request sequence")
        exchanges_by_sequence[sequence] = exchange

    result_usage_excluded: set[int] = set()
    matched_sequences: set[int] = set()
    for group in groups.values():
        if len(group["codex"]) != 1:
            raise ValueError("client and provider capture boundaries do not reconcile")
        front = group["codex"][0]
        provider_expected = front.get("provider_expected")
        if provider_expected is not None and provider_expected is not False:
            raise ValueError("raw provider expectation is invalid")
        if not group["provider"] and provider_expected is not False:
            raise ValueError("client and provider capture boundaries do not reconcile")
        providers = sorted(group["provider"], key=lambda item: item.get("provider_attempt", 0))
        if [item.get("provider_attempt") for item in providers] != list(range(1, len(providers) + 1)):
            raise ValueError("provider attempts are missing or duplicated")
        if any(item.get("request_sequence") != front.get("request_sequence") for item in providers):
            raise ValueError("request sequence changed across capture boundaries")
        front_sequence = front.get("request_sequence")
        if front_sequence in matched_sequences:
            raise ValueError("raw capture reuses a request sequence")
        exchange = exchanges_by_sequence.get(front_sequence)
        if exchange is None or exchange.get("thread_id") != front.get("thread_id"):
            raise ValueError("raw capture does not reconcile a metrics exchange")
        request_kind = front.get("request_kind")
        if request_kind not in (None, "turn", "prewarm", "compaction"):
            raise ValueError("capture has an unsupported request kind")
        if exchange.get("request_kind") != request_kind:
            raise ValueError("raw request kind differs from metrics")
        if any(provider.get("request_kind") != request_kind for provider in providers):
            raise ValueError("request kind changed across capture boundaries")
        if provider_expected is False:
            result_usage_excluded.add(front_sequence)
        matched_sequences.add(front_sequence)
        attempts = exchange.get("provider_attempts")
        if not isinstance(attempts, list) or len(attempts) != len(providers):
            raise ValueError("raw provider attempts differ from the metrics exchange")
        if (
            front.get("predecessor_sequence", 0) != exchange.get("predecessor_sequence", 0)
            or front.get("cache_fingerprint") != exchange.get("client_fingerprint")
            or payload(front.get("request")) != payload(exchange.get("client_request"))
            or payload(front.get("response")) != payload(exchange.get("client_response"))
            or payload(front.get("final_output")) != payload(exchange.get("client_final_output"))
            or payload(front.get("final_text")) != payload(exchange.get("client_final_text"))
            or front.get("tool_calls", []) != exchange.get("delivered_tools", [])
        ):
            raise ValueError("raw client measurements differ from the metrics exchange")
        for raw, measured in zip(providers, attempts, strict=True):
            if raw.get("provider_response") != measured.get("provider_response"):
                raise ValueError("raw provider response evidence differs from snapshot")
            validate_provider_evidence(raw.get("provider_response"), raw.get("usage"))
            if raw.get("request_model") != measured.get("model"):
                raise ValueError("raw provider model differs from the metrics exchange")
            raw_usage = raw.get("usage")
            measured_usage = measured.get("usage")
            if (raw_usage is None) != (measured_usage is None):
                raise ValueError("raw provider usage presence differs from the metrics exchange")
            if raw_usage is not None and usage(raw_usage) != usage(measured_usage):
                raise ValueError("raw provider usage differs from the metrics exchange")
            if (
                raw.get("predecessor_sequence", 0) != front.get("predecessor_sequence", 0)
                or raw.get("transport") != measured.get("transport")
                or raw.get("cache_fingerprint") != measured.get("cache_fingerprint")
                or raw.get("projected_fingerprint") != measured.get("projected_fingerprint")
                or payload(raw.get("request")) != payload(measured.get("request"))
                or payload(raw.get("projected_request")) != payload(measured.get("projected_request"))
                or payload(raw.get("response")) != payload(measured.get("response"))
                or payload(raw.get("final_output")) != payload(measured.get("final_output"))
                or payload(raw.get("final_text")) != payload(measured.get("final_text"))
                or raw.get("tool_calls", []) != measured.get("tools", [])
            ):
                raise ValueError("raw provider measurements differ from the metrics exchange")
    if matched_sequences != set(exchanges_by_sequence):
        raise ValueError("metrics exchanges differ from raw capture groups")

    published_transport = metrics.get("transport")
    if not isinstance(published_transport, dict) or set(published_transport) != set(TRANSPORT_KEYS):
        raise ValueError("metrics have an unsupported transport shape")
    if any(payload(published_transport.get(key)) != control_transport[key] for key in CONTROL_TRANSPORT_KEYS):
        raise ValueError("raw control transport differs from the metrics snapshot")

    health = metrics.get("capture")
    if not isinstance(health, dict):
        raise ValueError("metrics are missing capture health")
    if health.get("records") != len(records):
        raise ValueError("raw capture count differs from the metrics snapshot")
    error_keys = (
        "capture_errors",
        "incomplete_records",
        "missing_provider_records",
        "provider_attempt_gaps",
        "write_errors",
        "skipped_requests",
        "dropped_exchange_details",
    )
    if any(health.get(key) != 0 for key in error_keys):
        raise ValueError("capturer health reports incomplete evidence")
    return result_usage_excluded


def validate_snapshot(metrics: dict[str, Any], arm: str, config: dict[str, Any]) -> None:
    if metrics.get("schema") != "mekugi.capture.metrics.v6":
        raise ValueError("metrics have an unsupported schema")
    expected = EXPECTED_ARM_CONFIG.get(arm)
    if expected is None:
        raise ValueError(f"unsupported benchmark arm {arm}")
    if metrics.get("mode") != expected:
        raise ValueError(f"{arm} capture has the wrong router mode")
    exchanges = metrics.get("exchanges")
    if not isinstance(exchanges, list):
        raise ValueError("metrics are missing exchanges")
    validate_calculations(metrics, exchanges)


def validate_published_usage(value: object, parsed: dict[str, int], provider_attempts: int) -> None:
    if not isinstance(value, dict):
        raise ValueError("published usage must be an object")
    if value.get("uncached_input_tokens") != parsed["input_tokens"] - parsed["cached_input_tokens"]:
        raise ValueError("published uncached input does not reconcile total and cached input")
    if value.get("provider_attempts") != provider_attempts:
        raise ValueError("published usage-bearing attempt count does not reconcile provider evidence")


def validate_calculations(metrics: dict[str, Any], exchanges: list[dict[str, Any]]) -> None:
    def sequence(exchange: object) -> int:
        if not isinstance(exchange, dict):
            raise ValueError("exchange must be an object")
        value = exchange.get("sequence")
        if type(value) is not int or value < 0:
            raise ValueError("exchange sequence must be a non-negative integer")
        return value

    ordered = sorted(exchanges, key=sequence)
    requests = {"logical": len(ordered), "provider_attempts": 0, "completed": 0, "failed": 0}
    calculated_usage = empty_usage()
    usage_attempts = 0
    transport = {key: empty_payload() for key in INFERENCE_TRANSPORT_KEYS}
    semantic = {
        "provider_attempt_outputs": empty_payload(),
        "client_outputs": empty_payload(),
    }
    provider_tools: dict[str, dict[str, int]] = {}
    delivered_tools: dict[str, dict[str, int]] = {}
    previous_input: dict[str, int] = {}
    cold_or_new = eligible = eligible_cached = 0

    for exchange in ordered:
        attempts = exchange.get("provider_attempts")
        if not isinstance(attempts, list):
            raise ValueError("exchange is missing provider attempts")
        requests["provider_attempts"] += len(attempts)
        outcome = "completed" if exchange.get("status") == "completed" else "failed"
        requests[outcome] += 1
        add_payload(transport["client_requests"], exchange.get("client_request"))
        add_payload(transport["client_responses"], exchange.get("client_response"))
        add_payload(semantic["client_outputs"], exchange.get("client_final_output"))
        delivered = exchange.get("delivered_tools", [])
        if not isinstance(delivered, list):
            raise ValueError("exchange delivered tools must be an array")
        for call in delivered:
            add_tool(delivered_tools, call)
        exchange_usage = empty_usage()
        exchange_usage_attempts = 0
        final_usage = None
        for attempt in attempts:
            if not isinstance(attempt, dict):
                raise ValueError("provider attempt must be an object")
            if not isinstance(attempt.get("model"), str) or not attempt["model"]:
                raise ValueError("provider attempt is missing its actual model")
            if not isinstance(attempt.get("projected_request"), dict):
                raise ValueError("missing projected request observation")
            payload(attempt["projected_request"])
            transport_kind = attempt.get("transport")
            if transport_kind not in {None, "websocket"}:
                raise ValueError("provider attempt has an unsupported transport")
            published_attempt_usage = attempt.get("usage")
            parsed_attempt_usage = None
            if published_attempt_usage is not None:
                parsed_attempt_usage = usage(published_attempt_usage)
                add(exchange_usage, parsed_attempt_usage)
                add(calculated_usage, parsed_attempt_usage)
                exchange_usage_attempts += 1
                usage_attempts += 1
                validate_published_usage(published_attempt_usage, parsed_attempt_usage, 1)
            final_usage = parsed_attempt_usage
            add_payload(transport["provider_attempt_requests"], attempt.get("request"))
            add_payload(transport["provider_responses"], attempt.get("response"))
            add_payload(semantic["provider_attempt_outputs"], attempt.get("final_output"))
            tools = attempt.get("tools", [])
            if not isinstance(tools, list):
                raise ValueError("provider attempt tools must be an array")
            for emitted in tools:
                add_tool(provider_tools, emitted)

        published_exchange_usage = exchange.get("usage")
        if exchange_usage_attempts:
            if usage(published_exchange_usage) != exchange_usage:
                raise ValueError("exchange usage does not reconcile provider attempts")
            validate_published_usage(published_exchange_usage, exchange_usage, exchange_usage_attempts)
        elif published_exchange_usage is not None:
            raise ValueError("exchange publishes usage without provider evidence")

        thread = exchange.get("thread_id")
        if not isinstance(thread, str) or not thread:
            if final_usage is not None:
                cold_or_new += final_usage["input_tokens"] - final_usage["cached_input_tokens"]
        elif final_usage is None:
            previous_input.pop(thread, None)
        else:
            current_eligible = min(previous_input.get(thread, 0), final_usage["input_tokens"])
            current_cached = min(current_eligible, final_usage["cached_input_tokens"])
            current_miss = current_eligible - current_cached
            eligible += current_eligible
            eligible_cached += current_cached
            cold_or_new += final_usage["input_tokens"] - final_usage["cached_input_tokens"] - current_miss
            previous_input[thread] = final_usage["input_tokens"]

    validate_cache_diagnostics(exchanges)

    if metrics.get("requests") != requests:
        raise ValueError("request totals do not reconcile exchanges")
    published_usage = metrics.get("usage")
    if usage(published_usage) != calculated_usage:
        raise ValueError("aggregate usage does not reconcile exchanges")
    validate_published_usage(published_usage, calculated_usage, usage_attempts)
    published_transport = metrics.get("transport")
    if not isinstance(published_transport, dict) or set(published_transport) != set(TRANSPORT_KEYS):
        raise ValueError("metrics have an unsupported transport shape")
    if (
        any(payload(published_transport.get(key)) != transport[key] for key in INFERENCE_TRANSPORT_KEYS)
        or metrics.get("semantic") != semantic
    ):
        raise ValueError("payload totals do not reconcile exchanges")
    if metrics.get("provider_tools") != provider_tools or metrics.get("delivered_tools") != delivered_tools:
        raise ValueError("tool aggregates do not reconcile exchanges")

    published_cache = metrics.get("cache")
    if not isinstance(published_cache, dict):
        raise ValueError("metrics are missing cache calculations")
    eligible_miss = eligible - eligible_cached
    expected_cache = {
        "cold_or_new_uncached_input_tokens": cold_or_new,
        "eligible_prefix_tokens": eligible,
        "eligible_prefix_cached_tokens": eligible_cached,
        "eligible_prefix_miss_tokens": eligible_miss,
    }
    if any(published_cache.get(key) != value for key, value in expected_cache.items()):
        raise ValueError("cache attribution does not reconcile final provider attempts")
    validate_rate(
        published_cache.get("provider_cache_rate"),
        calculated_usage["cached_input_tokens"],
        calculated_usage["input_tokens"],
        "provider cache rate",
    )
    validate_rate(
        published_cache.get("eligible_prefix_cache_rate"),
        eligible_cached,
        eligible,
        "eligible prefix cache rate",
    )


def required_text(value: object, description: str) -> str:
    if not isinstance(value, str) or not value:
        raise ValueError(f"{description} must be a nonempty string")
    return value


def validate_results(
    metrics: dict[str, Any],
    results_path: Path,
    arm: str,
    config: dict[str, Any],
    result_usage_excluded: set[int] | None = None,
) -> int:
    result_usage_excluded = result_usage_excluded or set()
    expected: dict[str, dict[str, int]] = {}
    allowed_models: dict[str, set[str]] = {}
    main_models: dict[str, str] = {}
    mentor_threads: dict[str, str] = {}
    for result_record in load_jsonl(results_path):
        if result_record.get("arm") != arm:
            continue
        agent = result_record.get("agent")
        thread = agent.get("thread_id") if isinstance(agent, dict) else None
        if not isinstance(thread, str) or not thread or thread in expected:
            raise ValueError("results have a missing or duplicate measured thread")
        expected[thread] = usage(agent.get("usage"), result=True)
        configured_model = required_text(result_record.get("model"), "result model")
        parent_model = result_record.get("parent_model") or configured_model
        parent_model = required_text(parent_model, "result parent model")
        allowed_models[thread] = {parent_model}

        main_mentor = config.get("main_mentor", {})
        if main_mentor.get("enabled"):
            if config.get("benchmark_mode") != "mekugi-diagnostic" or arm != "mekugi":
                raise ValueError("main mentor requires the diagnostic Mekugi arm")
            if parent_model not in {"gpt-5.6", "gpt-5.6-sol"} or main_mentor.get("requested_model") != parent_model:
                raise ValueError("main mentor configured model mismatch")
            if main_mentor.get("requested_reasoning_effort") != result_record.get("reasoning_effort"):
                raise ValueError("main mentor configured reasoning effort mismatch")
            if main_mentor.get("model") != "gpt-6-astra":
                raise ValueError("unsupported main mentor model")
            allowed_models[thread].add("gpt-6-astra")
            main_models[thread] = parent_model
            mentor_threads[thread] = "gpt-6-astra"

        child_model = result_record.get("child_model")
        proof_value = agent.get("child_proof_path") if isinstance(agent, dict) else None
        mentor_result = child_model is not None or proof_value is not None or arm == "mekugi-mentor"
        if not mentor_result:
            continue
        child_model = required_text(child_model, "mentor child model")
        proof_path = Path(required_text(proof_value, "mentor child proof path"))
        if not proof_path.is_absolute():
            proof_path = results_path.parent / proof_path
        proof = load_json(proof_path)
        if proof.get("schema") != "mekugi.benchmark.child-proof.v1":
            raise ValueError("mentor child proof has an unsupported schema")
        child_thread = required_text(proof.get("child_thread_id"), "mentor child thread")
        if child_thread in allowed_models:
            raise ValueError("mentor child thread is duplicated")
        if proof.get("configured_model") != child_model:
            raise ValueError("mentor child proof disagrees with the configured model")
        child_effort = required_text(result_record.get("child_reasoning_effort"), "mentor child effort")
        if proof.get("configured_reasoning_effort") != child_effort:
            raise ValueError("mentor child proof disagrees with the configured reasoning effort")
        allowed_models[child_thread] = {child_model}
        if arm == "mekugi-mentor":
            # The router's child mentor is independent of the benchmark's main model.
            mentor_model = required_text(config.get("mentor_handoff", {}).get("mentor_model"), "mentor model")
            allowed_models[child_thread].add(mentor_model)
            mentor_threads[child_thread] = mentor_model
    if not expected:
        raise ValueError(f"results contain no {arm} records")

    observed = {thread: empty_usage() for thread in expected}
    seen: set[str] = set()
    actual_models: dict[str, set[str]] = defaultdict(set)
    # Snapshots retain completion order; handoff follows request sequence.
    for exchange in sorted(metrics["exchanges"], key=lambda item: item["sequence"]):
        thread = exchange.get("thread_id")
        if thread not in allowed_models:
            raise ValueError(f"capture contains an unproved thread {thread}")
        attempts = exchange.get("provider_attempts")
        if not isinstance(attempts, list):
            raise ValueError("exchange is missing provider attempts")
        for attempt in attempts:
            model = attempt.get("model") if isinstance(attempt, dict) else None
            if model not in allowed_models[thread]:
                raise ValueError(f"provider model {model} violates the configured schedule")
            # Non-turn requests do not advance the schedule. Compaction usage
            # still counts in the result; prewarm usage remains aggregate-only.
            if thread in main_models and (
                exchange.get("request_kind") in {"prewarm", "compaction"}
                or exchange["sequence"] in result_usage_excluded
            ):
                continue
            if thread in main_models:
                configured = main_models[thread]
                if model == configured and "gpt-6-astra" not in actual_models[thread]:
                    raise ValueError("main schedule did not start with Astra")
                if model == "gpt-6-astra" and configured in actual_models[thread]:
                    raise ValueError("main schedule restarted Astra after handoff")
            actual_models[thread].add(model)
        if thread in observed:
            seen.add(thread)
            if exchange.get("usage") is not None and exchange.get("sequence") not in result_usage_excluded:
                add(observed[thread], usage(exchange["usage"]))
    for thread, expected_usage in expected.items():
        if thread not in seen:
            raise ValueError(f"capture has no request for measured thread {thread}")
        if observed[thread] != expected_usage:
            raise ValueError(f"captured provider usage differs from result usage for {thread}")
    for child_thread in allowed_models.keys() - expected.keys():
        if child_thread not in actual_models:
            raise ValueError("capture has no request for a proved mentor child")
    for child_thread, mentor_model in mentor_threads.items():
        if mentor_model not in actual_models[child_thread]:
            raise ValueError("mentor treatment never routed the scheduled thread to the mentor model")
    return len(expected)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("metrics", type=Path)
    parser.add_argument("capture", type=Path)
    parser.add_argument("results", type=Path)
    parser.add_argument("arm")
    args = parser.parse_args()
    try:
        config_path = args.results.parent / "benchmark-config.json"
        config = load_json(config_path) if config_path.exists() else {}
        metrics = load_json(args.metrics)
        validate_snapshot(metrics, args.arm, config)
        result_usage_excluded = validate_raw_capture(args.capture, metrics)
        runs = validate_results(metrics, args.results, args.arm, config, result_usage_excluded)
    except (OSError, ValueError, json.JSONDecodeError) as error:
        parser.error(str(error))
    json.dump(
        {
            "schema": "mekugi.benchmark.capture-validation.v1",
            "arm": args.arm,
            "runs": runs,
            "records": metrics["capture"]["records"],
            "logical_requests": metrics["requests"]["logical"],
            "provider_attempts": metrics["requests"]["provider_attempts"],
            "valid": True,
        },
        sys.stdout,
        sort_keys=True,
        separators=(",", ":"),
    )
    sys.stdout.write("\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
