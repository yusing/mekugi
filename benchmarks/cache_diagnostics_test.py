#!/usr/bin/env python3
"""Model-free checks for sanitized cache-diagnostic evidence."""
import copy
import sys
import unittest

sys.dont_write_bytecode = True
from analyze_capture import compare_prefix, validate_cache_diagnostics, validate_fingerprint, validate_provider_evidence


def fingerprint(items):
    return {"scope": "a" * 32, "fields": {"model": "b" * 32, "other":"f" * 32},
            "input_kind": "array", "items": items, "item_count": len(items),
            "complete": True, "routing_key": "c" * 32, "request_key": "c" * 32}


def exchange(sequence, fp, diagnosis):
    return {"sequence": sequence, "predecessor_sequence": sequence-1, "thread_id": "thread", "status": "completed",
            "client_fingerprint": fp, "provider_attempts": [
                {"cache_fingerprint": fp, "projected_fingerprint": fp}],
            "cache_diagnostics": diagnosis}


class CacheDiagnosticsTests(unittest.TestCase):
    def test_ordered_stage_comparison_and_tampering(self):
        a, b = fingerprint(["d" * 32]), fingerprint(["d" * 32, "e" * 32])
        unavailable = {"status": "unavailable", "common_items": 0, "changed_fields": []}
        appended = {"status": "appended", "common_items": 1, "changed_fields": []}
        first = {"previous_sequence": 0, "client": unavailable, "projected": unavailable,
                 "provider": unavailable, "routing": "unavailable", "request_key": "unavailable"}
        second = {"previous_sequence": 1, "client": appended, "projected": appended,
                  "provider": appended, "routing": "stable", "request_key": "stable"}
        rows = [exchange(2, b, second), exchange(1, a, first)]
        validate_cache_diagnostics(rows)
        broken = copy.deepcopy(rows)
        broken[0]["cache_diagnostics"]["routing"] = "changed"
        with self.assertRaises(ValueError):
            validate_cache_diagnostics(broken)
        broken = copy.deepcopy(rows)
        broken[0]["provider_attempts"][0]["cache_fingerprint"]["items"][0] = "f" * 32
        with self.assertRaises(ValueError):
            validate_cache_diagnostics(broken)

    def test_turn_state_forwarding_and_tampering(self):
        unavailable = {"status": "unavailable", "common_items": 0, "changed_fields": []}
        for client, provider, expected in (("", "", "absent"), ("d"*32, "d"*32, "preserved"),
                                           ("d"*32, "", "dropped"), ("d"*32, "e"*32, "changed")):
            fp = fingerprint([])
            fp["turn_state"] = client
            diagnosis = {"previous_sequence": 0, "client": unavailable, "projected": unavailable,
                         "provider": unavailable, "routing": "unavailable", "request_key": "unavailable",
                         "turn_state_forwarding": expected}
            row = exchange(1, fp, diagnosis)
            row["provider_attempts"][0]["cache_fingerprint"] = copy.deepcopy(fp)
            row["provider_attempts"][0]["cache_fingerprint"]["turn_state"] = provider
            validate_cache_diagnostics([row])
            del row["thread_id"]
            validate_cache_diagnostics([row])
            row["cache_diagnostics"]["turn_state_forwarding"] = "wrong"
            with self.assertRaises(ValueError):
                validate_cache_diagnostics([row])
        for invalid in (None, "private state", 42):
            fp = fingerprint([])
            fp["turn_state"] = invalid
            with self.assertRaises(ValueError):
                validate_fingerprint(fp)

    def test_provider_response_evidence(self):
        for state in ("missing", "null", "invalid", "unavailable"):
            e = {"cached_tokens_state": state}
            validate_provider_evidence(e, None)
            e["cached_tokens"] = 0
            with self.assertRaises(ValueError):
                validate_provider_evidence(e, None)
        for count in (0, 128):
            e = {"cached_tokens_state": "present", "cached_tokens": count,
                 "request_id": "req-test", "model": "response-model", "header_model": "header-model"}
            validate_provider_evidence(e, {"cached_input_tokens": count})
            with self.assertRaises(ValueError):
                validate_provider_evidence(e, {"cached_input_tokens": count+1})
        for changes in ({"cached_tokens": None}, {"cached_tokens": -1}, {"request_id": "unsafe\ntext"},
                        {"authorization": "secret"}, {"model": "x"*257}):
            e = {"cached_tokens_state": "present", "cached_tokens": 0} | changes
            with self.assertRaises(ValueError):
                validate_provider_evidence(e, None)
        validate_provider_evidence(None, {"cached_input_tokens": 0})

    def test_private_shape_and_partial_evidence(self):
        fp = fingerprint(["d" * 32])
        validate_fingerprint(fp)
        partial = copy.deepcopy(fp)
        partial.update(item_count=129, complete=False, items=["d" * 32]*128)
        self.assertEqual(compare_prefix(fp, partial)["status"], "unavailable")
        malformed = copy.deepcopy(fp)
        malformed["input_kind"] = "absent"
        with self.assertRaises(ValueError):
            validate_fingerprint(malformed)
        malformed = copy.deepcopy(fp)
        del malformed["fields"]["other"]
        with self.assertRaises(ValueError):
            validate_fingerprint(malformed)
        unsafe = copy.deepcopy(fp)
        unsafe["routing_key"] = "raw secret key"
        with self.assertRaises(ValueError):
            validate_fingerprint(unsafe)
        unsafe = copy.deepcopy(fp)
        unsafe["fields"]["private arbitrary key"] = "d" * 32
        with self.assertRaises(ValueError):
            validate_fingerprint(unsafe)


if __name__ == "__main__":
    unittest.main()
