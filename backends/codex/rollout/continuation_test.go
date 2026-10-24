package rollout

import (
	json "encoding/json/v2"
	"os"
	"path/filepath"
	"testing"
)

func TestConfirmRetainedOutputRequiresHostProvenance(t *testing.T) {
	const output = `{"id":"wait-identity","state":"timeout"}`
	for _, test := range []struct {
		name                                                          string
		compacted, authored, inherited, call, mismatch, aligned, want bool
	}{
		{name: "ordinary", authored: true, want: true},
		{name: "compacted", compacted: true, authored: true, aligned: true, want: true},
		{name: "caller lookalike"},
		{name: "inherited", authored: true, inherited: true},
		{name: "tool call", authored: true, call: true},
		{name: "different output", authored: true, mismatch: true},
		{name: "unaligned sidecar", compacted: true, authored: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			item := map[string]any{"type": "function_call_output", "name": "wait_agent", "namespace": "threadcontrol", "output": output}
			if test.call {
				item["call_id"] = "tool-call"
			}
			if test.mismatch {
				item["output"] = output + " "
			}
			metadata := map[string]any{"client_authored": test.authored, "inherited_user_message": test.inherited}
			record := map[string]any{"type": "response_item", "payload": item, "metadata": metadata}
			if test.compacted {
				sidecar := []any{metadata}
				if !test.aligned {
					sidecar = nil
				}
				record = map[string]any{"type": "compacted", "payload": map[string]any{"replacement_history": []any{item}, "replacement_history_metadata": sidecar}}
			}
			encoded, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "rollout.jsonl")
			if err := os.WriteFile(path, append([]byte("{\"type\":\"session_meta\",\"payload\":{\"id\":\"thread\"}}\n"), append(encoded, '\n')...), 0600); err != nil {
				t.Fatal(err)
			}
			err = ConfirmRetainedOutput(t.Context(), "thread", path, "threadcontrol", "wait_agent", output)
			if (err == nil) != test.want {
				t.Fatalf("confirmation = %v, want success %v", err, test.want)
			}
			if err := ConfirmRetainedOutput(t.Context(), "foreign", path, "threadcontrol", "wait_agent", output); err == nil {
				t.Fatal("foreign history confirmed the continuation")
			}
		})
	}
}

func TestConfirmTerminalTurnUsesExactIdentity(t *testing.T) {
	for _, terminal := range []string{"task_complete", "turn_complete", "turn_aborted", "task_started"} {
		t.Run(terminal, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "rollout.jsonl")
			record, err := json.Marshal(map[string]any{"type": "event_msg", "payload": map[string]any{"type": terminal, "turn_id": "target"}})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, append([]byte("{\"type\":\"session_meta\",\"payload\":{\"id\":\"thread\"}}\n"), append(record, '\n')...), 0600); err != nil {
				t.Fatal(err)
			}
			if err := ConfirmTerminalTurn(t.Context(), "thread", path, "target"); (err == nil) != (terminal != "task_started") {
				t.Fatalf("terminal confirmation = %v", err)
			}
			if err := ConfirmTerminalTurn(t.Context(), "thread", path, "other"); err == nil {
				t.Fatal("unrelated turn confirmed interruption")
			}
			if err := ConfirmTerminalTurn(t.Context(), "foreign", path, "target"); err == nil {
				t.Fatal("foreign thread confirmed interruption")
			}
		})
	}
}

func TestConfirmHumanInputRequiresRetainedClientIdentity(t *testing.T) {
	for _, kind := range []string{"user_message", "agent_message"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "rollout.jsonl")
			record, err := json.Marshal(map[string]any{"type": "event_msg", "payload": map[string]any{"type": kind, "client_id": "human", "message": "same text"}})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, append([]byte("{\"type\":\"session_meta\",\"payload\":{\"id\":\"child\"}}\n"), append(record, '\n')...), 0600); err != nil {
				t.Fatal(err)
			}
			if err := ConfirmUserInput(t.Context(), "child", path, "human"); (err == nil) != (kind == "user_message") {
				t.Fatalf("confirmation: %v", err)
			}
			if err := ConfirmUserInput(t.Context(), "foreign", path, "human"); err == nil {
				t.Fatal("foreign history confirmed input")
			}
			if err := ConfirmUserInput(t.Context(), "child", path, "different"); err == nil {
				t.Fatal("text similarity confirmed different input")
			}
		})
	}
}
