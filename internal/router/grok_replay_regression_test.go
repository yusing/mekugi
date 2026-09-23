package router

import (
	"encoding/json"
	"testing"
)

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
