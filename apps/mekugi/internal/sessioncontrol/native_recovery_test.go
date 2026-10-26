package sessioncontrol

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func TestNativeInputRetainedRunRoundtrip(t *testing.T) {
	for _, name := range []string{"followup", "initial", "timeout"} {
		t.Run(name, func(t *testing.T) {
			transport := &requestTransport{id: "native"}
			requests := NewRequests[struct{}](transport)
			input := control.NativeInput{Thread: "child", ParentTurn: "parent-turn", ID: "root:operation", Sender: "/root", Recipient: "/root/task", Kind: "NEW_TASK", Message: "same assignment", Run: true}
			if name == "initial" {
				input.Settings, input.Message = &control.Settings{}, ""
			}
			if name == "timeout" {
				input.Wake = &control.WaitResult{ID: "wait", State: "timeout", Targets: []string{"/root/task"}, EffectiveMS: new(int64(10)), Metrics: map[string]control.SessionMetrics{"/root/z": {ContextTokens: new(uint64(21))}, "/root/a": {Compactions: new(uint64(2))}}}
			}
			done := make(chan error, 1)
			go func() {
				_, err := requests.Input(t.Context(), input)
				done <- err
			}()
			requests.Dispatch(<-requests.Calls())
			var params struct {
				Output struct {
					Namespace, Name, Output string
					Retain                  bool
				} `json:"toolOutput"`
			}
			if err := json.Unmarshal(transport.sends[0].params.(jsontext.Value), &params, json.MatchCaseInsensitiveNames(true)); err != nil {
				t.Fatal(err)
			}
			requests.CompleteCall(appserver.Message{ID: jsontext.Value(`native`), Result: jsontext.Value(`{"turn":{"id":"turn"}}`)})
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			wantName := "followup_task"
			if name == "initial" {
				wantName = "spawn_agent"
			} else if name == "timeout" {
				wantName = "wait_agent"
			}
			if !params.Output.Retain || params.Output.Name != wantName {
				t.Fatalf("run output was not retained as %s: %+v", wantName, params.Output)
			}
			if name == "timeout" && (!strings.Contains(params.Output.Output, `"/root/z":{"context_tokens":21,"context_window":null,"compactions":null}`) || strings.Contains(params.Output.Output, "_known")) {
				t.Fatalf("retained wait metrics = %s", params.Output.Output)
			}
			// Persist the actual host request payload in the host's rollout shape.
			path := filepath.Join(t.TempDir(), "rollout.jsonl")
			record, err := json.Marshal(map[string]any{
				"type":     "response_item",
				"payload":  map[string]any{"type": "function_call_output", "namespace": params.Output.Namespace, "name": params.Output.Name, "output": params.Output.Output},
				"metadata": map[string]any{"client_authored": true},
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, append([]byte("{\"type\":\"session_meta\",\"payload\":{\"id\":\"child\"}}\n"), append(record, '\n')...), 0600); err != nil {
				t.Fatal(err)
			}
			if err := ConfirmNativeInput(t.Context(), path, input); err != nil {
				t.Fatalf("host-retained run did not confirm: %v", err)
			}
			input.ID = "root:new-operation"
			if err := ConfirmNativeInput(t.Context(), path, input); err == nil {
				t.Fatal("older identical assignment confirmed a different operation")
			}
			if len(transport.sends) != 1 {
				t.Fatal("confirmation dispatched host work")
			}
		})
	}
}

func TestNativeInputInterruptRequiresExactTerminalTurn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	if err := os.WriteFile(path, []byte("{\"type\":\"session_meta\",\"payload\":{\"id\":\"child\"}}\n"+
		"{\"type\":\"event_msg\",\"payload\":{\"type\":\"turn_aborted\",\"turn_id\":\"target-turn\"}}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	input := control.NativeInput{Thread: "child", ID: "root:interrupt", Kind: "INTERRUPT", TargetTurn: "target-turn"}
	if err := ConfirmNativeInput(t.Context(), path, input); err != nil {
		t.Fatal(err)
	}
	input.TargetTurn = "other-turn"
	if err := ConfirmNativeInput(t.Context(), path, input); err == nil {
		t.Fatal("unrelated terminal turn confirmed interruption")
	}
}
