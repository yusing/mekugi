package rollout

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func prefixFile(t *testing.T, thread, rollout string, rows ...map[string]any) (PrefixSource, []byte) {
	t.Helper()
	var data []byte
	for _, row := range rows {
		encoded, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, append(encoded, '\n')...)
	}
	path := filepath.Join(t.TempDir(), "rollout-"+rollout+".jsonl")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return PrefixSource{Thread: thread, Rollout: rollout, Path: path}, data
}

func TestReadPrefixFreezesActiveExecutionAndRejectsStaleIdentity(t *testing.T) {
	rows := []map[string]any{
		{"type": "session_meta", "payload": map[string]any{"id": "parent", "history_mode": "legacy"}},
		{"type": "turn_context", "payload": map[string]any{"turn_id": "active"}},
		{"type": "response_item", "payload": map[string]any{"type": "custom_tool_call", "id": "spawn-exec", "call_id": "exec-call", "name": "exec", "input": "await spawn()"}},
		{"type": "response_item", "payload": map[string]any{"type": "message", "role": "user", "content": "later input"}},
	}
	source, original := prefixFile(t, "parent", "rollout", rows...)
	got, err := ReadPrefix(t.Context(), source, PrefixBoundary{Turn: "active", Item: "spawn-exec"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "later input") || !strings.Contains(string(got), "spawn-exec") {
		t.Fatal("prefix missed or crossed the active execution boundary")
	}
	for _, boundary := range []PrefixBoundary{{Turn: "stale", Item: "spawn-exec"}, {Turn: "active", Item: "missing"}} {
		if _, err := ReadPrefix(t.Context(), source, boundary, nil); err == nil {
			t.Fatal("missing or stale boundary was accepted")
		}
	}
	after, err := os.ReadFile(source.Path)
	if err != nil || !bytes.Equal(after, original) {
		t.Fatal("prefix capture modified its source")
	}
	rows[0]["payload"].(map[string]any)["history_mode"] = "unknown"
	unknown, _ := prefixFile(t, "parent", "unknown", rows...)
	if _, err := ReadPrefix(t.Context(), unknown, PrefixBoundary{Turn: "active", Item: "spawn-exec"}, nil); err == nil {
		t.Fatal("unknown history format was treated as legacy")
	}
}

func TestReadPrefixMaterializesReferencedRevertedRollout(t *testing.T) {
	base, baseBytes := prefixFile(t, "stable-thread", "reverted-rollout",
		map[string]any{"ordinal": 0, "type": "session_meta", "payload": map[string]any{"id": "stable-thread", "history_mode": "paginated"}},
		map[string]any{"ordinal": 1, "type": "response_item", "payload": map[string]any{"type": "message", "role": "user", "content": "inherited"}},
	)
	leafRows := []map[string]any{
		{"ordinal": 2, "type": "session_meta", "payload": map[string]any{"id": "parent", "history_mode": "paginated", "history_base": map[string]any{"thread_id": base.Rollout, "end_ordinal_exclusive": 2, "end_byte_offset": len(baseBytes)}}},
		{"ordinal": 3, "type": "turn_context", "payload": map[string]any{"turn_id": "active"}},
		{"ordinal": 4, "type": "response_item", "payload": map[string]any{"type": "custom_tool_call", "id": "spawn-exec", "call_id": "exec-call"}},
	}
	leaf, _ := prefixFile(t, "parent", "leaf", leafRows...)
	resolve := func(_ context.Context, id string) (PrefixSource, error) {
		if id != "reverted-rollout" {
			t.Fatalf("resolved thread identity instead of rollout identity: %s", id)
		}
		return base, nil
	}
	got, err := ReadPrefix(t.Context(), leaf, PrefixBoundary{Turn: "active", Item: "spawn-exec"}, resolve)
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]jsontext.Value
	for line := range bytes.SplitSeq(bytes.TrimSpace(got), []byte{'\n'}) {
		var row map[string]jsontext.Value
		if err := json.Unmarshal(line, &row); err != nil {
			t.Fatal(err)
		}
		if row["ordinal"] != nil {
			t.Fatal("legacy import retained paginated ordinals")
		}
		rows = append(rows, row)
	}
	if len(rows) != 4 || !bytes.Contains(rows[0]["payload"], []byte(`"id":"parent"`)) || !bytes.Contains(rows[1]["payload"], []byte("inherited")) || bytes.Contains(got, []byte("history_base")) || bytes.Contains(got, []byte("paginated")) {
		t.Fatalf("materialized prefix: %s", got)
	}
	// A reference requires both the byte boundary and its exclusive ordinal.
	leafRows[0]["payload"].(map[string]any)["history_base"].(map[string]any)["end_ordinal_exclusive"] = 3
	bad, _ := prefixFile(t, "parent", "bad", leafRows...)
	if _, err := ReadPrefix(t.Context(), bad, PrefixBoundary{Turn: "active", Item: "spawn-exec"}, resolve); err == nil {
		t.Fatal("conflicting reference boundaries were accepted")
	}
}

func TestReadPrefixPreservesUnknownHostFields(t *testing.T) {
	rows := []map[string]any{
		{"type": "session_meta", "payload": map[string]any{"id": "parent", "future": map[string]any{"number": uint64(1<<60 + 1)}}},
		{"type": "compacted", "payload": map[string]any{"replacement_history": []any{map[string]any{"type": "message", "role": "user", "content": "retained"}}}},
		{"type": "turn_context", "payload": map[string]any{"turn_id": "active"}},
		{"type": "response_item", "metadata": map[string]any{"future": true}, "payload": map[string]any{"type": "custom_tool_call", "id": "spawn", "call_id": "call", "input": "exact\ninput", "unknown": nil}},
	}
	source, _ := prefixFile(t, "parent", "parent", rows...)
	got, err := ReadPrefix(t.Context(), source, PrefixBoundary{Turn: "active", Item: "spawn"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"number":1152921504606846977`, `"replacement_history"`, `"input":"exact\ninput"`, `"metadata":{"future":true}`, `"unknown":null`} {
		if !bytes.Contains(got, []byte(field)) {
			t.Errorf("prefix lost %s: %s", field, got)
		}
	}
}
