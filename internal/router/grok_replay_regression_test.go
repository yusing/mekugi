package router

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestGrokShellStreamDurableReplay(t *testing.T) {
	transform, proxy, _, workspace := newMekugiTestTransform(t, testTranslator(t, new(int)))
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	proxy.commentaryEndpoint = "http://localhost/internal/commentary"
	proxy.replayStore = store
	tr := &grokTranslation{tools: map[string]grokTool{"tool": {kind: "custom", name: "shell"}}}
	stream := grokTestSSE(map[string]any{"choices": []any{map[string]any{
		"index": 0, "delta": map[string]any{"content": "I'll start by reading the README.", "tool_calls": []any{map[string]any{
			"index": 0, "id": "call-test", "function": map[string]string{"name": "tool", "arguments": `{"input":"echo '<test>&' >/dev/null"}`},
		}}}, "finish_reason": "tool_calls",
	}}})
	bridge := &subagentBridge{}
	var streamedText strings.Builder
	var messageStarted bool
	var messageCompleted bool
	_, err = tr.readGrokStream(strings.NewReader(stream), func(event map[string]any) error {
		bridged, err := bridge.TransformSSE(mustMarshalJSON(event))
		if err != nil {
			return err
		}
		visible, err := transform.TransformSSE(bridged[0])
		for _, payload := range visible {
			var event struct {
				Type  string `json:"type"`
				Delta string `json:"delta"`
				Item  struct {
					Type string `json:"type"`
				} `json:"item"`
			}
			if err := json.Unmarshal(payload, &event); err != nil {
				t.Fatal(err)
			}
			if event.Type == "response.output_text.delta" {
				streamedText.WriteString(event.Delta)
			}
			if event.Item.Type != "message" {
				continue
			}
			switch event.Type {
			case "response.output_item.added":
				if messageStarted || messageCompleted {
					t.Fatal("message started twice")
				}
				messageStarted = true
			case "response.output_item.done":
				if !messageStarted || messageCompleted {
					t.Fatal("message completed out of order or twice")
				}
				messageCompleted = true
			}
		}
		if err != nil {
			return fmt.Errorf("%s: %w", event["type"], err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if streamedText.String() != "I'll start by reading the README." {
		t.Fatalf("provider text duplicated or lost: %q", streamedText.String())
	}
	if !messageCompleted {
		t.Fatal("provider message was lost")
	}
	restarted, err := openMekugiReplayStore(store.directory)
	if err != nil {
		t.Fatal(err)
	}
	history, found, err := restarted.lookup(t.Context(), workspace, "call-test")
	if err != nil || !found || history.Script != "echo '<test>&' >/dev/null" ||
		jsonString(history.UpstreamItem, "status") != "completed" {
		t.Fatalf("completed call did not survive restart: found=%v err=%v", found, err)
	}
}

func TestSubagentBridgePreservesReplayFieldEncoding(t *testing.T) {
	bridge := &subagentBridge{names: map[string]bool{"spawn_agent": true}}
	for _, item := range []string{
		`{"type":"custom_tool_call","name":"shell","input":"echo '<>&'","call_id":"c"}`,
		`{"type":"custom_tool_call","name":"shell","input":"echo '\u003c\u003e\u0026'","call_id":"c"}`,
		`{"type":"function_call","namespace":"mekugi_collaboration","name":"spawn_agent","arguments":"{\"message\":\"<>&\"}","call_id":"c"}`,
	} {
		t.Run(item, func(t *testing.T) {
			var original map[string]json.RawMessage
			if err := json.Unmarshal([]byte(item), &original); err != nil {
				t.Fatal(err)
			}
			for _, kind := range []string{"response.output_item.done", "response.completed", "response.failed", "response.incomplete"} {
				event := map[string]any{"type": kind}
				if kind == "response.output_item.done" {
					event["item"] = json.RawMessage(item)
				} else {
					event["response"] = map[string]any{"output": []json.RawMessage{json.RawMessage(item)}}
				}
				visible, err := bridge.TransformSSE(mustMarshalJSON(event))
				if err != nil {
					t.Fatal(err)
				}
				var envelope struct {
					Item     map[string]json.RawMessage `json:"item"`
					Response struct {
						Output []map[string]json.RawMessage `json:"output"`
					} `json:"response"`
				}
				if err := json.Unmarshal(visible[0], &envelope); err != nil {
					t.Fatal(err)
				}
				got := envelope.Item
				if got == nil {
					got = envelope.Response.Output[0]
				}
				for _, field := range []string{"input", "arguments"} {
					if string(got[field]) != string(original[field]) {
						t.Fatalf("%s changed %s encoding: %s != %s", kind, field, got[field], original[field])
					}
				}
			}
		})
	}
}
