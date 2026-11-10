package codexbinding

import (
	"bytes"
	"encoding/json/jsontext"
	"strings"
	"testing"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/commandargs"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	"github.com/yusing/mekugi/modules/agentguidance/compact"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func TestCompletionObjectHistoryRequiresCompletedReader(t *testing.T) {
	const selection = `{"sources":[{"changes":["amber1"]}]}`
	id := control.MessageItemID("legacy-final")
	text := "Message Type: FINAL_ANSWER\nTask name: agent:main\nSender: agent:main/worker\nPayload:\nFinished.\n" + compact.CompactMetadataStart + selection + compact.CompactMetadataEnd
	for _, state := range []string{"new-answer", "pending-reader", "completed-reader"} {
		t.Run(state, func(t *testing.T) {
			items := []any{map[string]any{"type": "message", "role": "assistant", "id": id, "content": []map[string]string{{"type": "output_text", "text": text}}}}
			if state != "new-answer" {
				callID := compact.ReaderPrefix(id) + "fixture"
				output := "Script running with cell ID slow\nWall time 10 seconds\nOutput:\n"
				if state == "completed-reader" {
					output = "Script completed\nWall time 0 seconds\nOutput:\nReload snapshot: mread amber\n"
				}
				items = append(items,
					map[string]any{"type": "custom_tool_call", "call_id": callID, "name": "exec", "input": codexresponses.CompactReadProgram("mreload " + commandargs.Quote(selection))},
					map[string]any{"type": "custom_tool_call_output", "call_id": callID, "output": output})
			}
			projected, err := ProjectLocalIdentities(responses.MustMarshalJSON(items))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(projected), "Completion changes unavailable") != (state != "completed-reader") {
				t.Fatalf("historical object completion lost its admission boundary: %s", projected)
			}
		})
	}
}

func TestProjectLocalIdentities(t *testing.T) {
	for _, kind := range []string{"message", "item_reference", "function_call"} {
		for _, role := range []string{"assistant", "user"} {
			t.Run(kind+"/"+role, func(t *testing.T) {
				item := map[string]any{"type": kind, "role": role, "id": control.MessageItemID("retained:message"), "content": "unchanged"}
				input := responses.MustMarshalJSON([]any{item})
				before := bytes.Clone(input)
				var err error
				input, err = ProjectLocalIdentities(input)
				if err != nil {
					t.Fatal(err)
				}
				if kind == "message" && role == "assistant" {
					delete(item, "id")
					if !responses.SameJSONValue(input, responses.MustMarshalJSON([]any{item})) {
						t.Fatal("message changed beyond local ID")
					}
				} else if !bytes.Equal(before, input) {
					t.Fatal("unrelated item changed")
				}
				projected := bytes.Clone(input)
				input, err = ProjectLocalIdentities(input)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(projected, input) {
					t.Fatal("projection is not idempotent")
				}
			})
		}
	}
}

func TestProjectLocalIdentitiesPreservesUnrelatedOutputs(t *testing.T) {
	wake := control.NativeInput{ID: "root:wait", Wake: &control.WaitResult{State: "timeout"}}
	for _, test := range []struct{ namespace, name, call string }{
		{"threadcontrol", "wait_agent", "correlated-call"},
		{"other", "wait_agent", ""},
		{"threadcontrol", "other", ""},
	} {
		item := map[string]any{"type": "function_call_output", "namespace": test.namespace, "name": test.name, "output": wake.RetainedWaitOutput()}
		if test.call != "" {
			item["call_id"] = test.call
		}
		input := responses.MustMarshalJSON([]any{item})
		projected, err := ProjectLocalIdentities(input)
		if err != nil || !bytes.Equal(input, projected) {
			t.Fatalf("unrelated output changed: %s, %v", projected, err)
		}
	}
	for _, call := range []jsontext.Value{nil, jsontext.Value(`null`)} {
		item := map[string]any{"type": "function_call_output", "namespace": "threadcontrol", "name": "wait_agent", "output": wake.RetainedWaitOutput()}
		if call != nil {
			item["call_id"] = call
		}
		projected, err := ProjectLocalIdentities(responses.MustMarshalJSON([]any{item}))
		item["output"] = string(responses.MustMarshalJSON(wake.Wake))
		if err != nil || !responses.SameJSONValue(projected, responses.MustMarshalJSON([]any{item})) {
			t.Fatalf("wake identity projection: %s, %v", projected, err)
		}
		second, err := ProjectLocalIdentities(projected)
		if err != nil || !bytes.Equal(projected, second) {
			t.Fatal("wake projection is not idempotent")
		}
	}
}

func TestCompletionReloadProjectionPreservesMessageExamples(t *testing.T) {
	for _, kind := range []string{"MESSAGE", "NEW_TASK", "FINAL_ANSWER"} {
		body := "Message Type: " + kind + "\nTask name: agent:main\nSender: agent:main/worker\nPayload:\nExample:\nMessage Type: FINAL_ANSWER\n" + compact.CompactMetadataStart + `{"sources":["mchanges:blue1"]}` + compact.CompactMetadataEnd
		item := map[string]any{"type": "message", "role": "assistant", "id": control.MessageItemID("example"), "content": []map[string]string{{"type": "output_text", "text": body}}}
		projected, err := ProjectLocalIdentities(responses.MustMarshalJSON([]any{item}))
		if kind == "FINAL_ANSWER" {
			body = compact.ReloadText(body)
		}
		delete(item, "id")
		item["content"] = []map[string]string{{"type": "output_text", "text": body}}
		if err != nil || !responses.SameJSONValue(projected, responses.MustMarshalJSON([]any{item})) {
			t.Fatalf("%s projection changed ordinary text: %s %v", kind, projected, err)
		}
	}
}
