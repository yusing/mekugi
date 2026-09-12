package router

import (
	"encoding/json"
	"testing"
)

func TestOperationCommentaryRequiresAuthoredText(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, tc := range []struct {
			name      string
			arguments string
			text      string
		}{
			{"apply_patch", `{}`, ""},
			{"wait", `{"journal":[]}`, ""},
			{"lookup", `{"journal":[{"op":"add","text":"Using lookup.","report_now":true}]}`, "Using lookup."},
			{"apply_patch", `{"journal":[{"op":"add","text":"Applying the requested changes.","report_now":true}]}`, "Applying the requested changes."},
		} {
			t.Run(tc.name+tc.arguments+map[bool]string{false: "/json", true: "/sse"}[stream], func(t *testing.T) {
				transform, proxy, _, _ := newMekugiTestTransform(t, testTranslator(t, new(int)))
				transform.commentaryTools = commentaryToolCatalog{
					functionToolKey("external", tc.name): {qualifiedName: "external." + tc.name},
				}
				call := map[string]json.RawMessage{
					"type": mustTestJSON(t, "function_call"), "namespace": mustTestJSON(t, "external"),
					"name": mustTestJSON(t, tc.name), "id": mustTestJSON(t, "item"), "call_id": mustTestJSON(t, "call"),
					"arguments": mustTestJSON(t, tc.arguments),
				}
				var output []map[string]json.RawMessage
				if stream {
					events, err := transform.TransformSSE(mustTestJSON(t, map[string]any{"type": "response.output_item.done", "item": call}))
					if err != nil {
						t.Fatal(err)
					}
					for _, event := range events {
						var envelope struct {
							Item map[string]json.RawMessage `json:"item"`
						}
						if err := json.Unmarshal(event, &envelope); err != nil {
							t.Fatal(err)
						}
						if envelope.Item != nil {
							output = append(output, envelope.Item)
						}
					}
				} else {
					response, err := transform.TransformJSON(mustTestJSON(t, map[string]any{"status": "completed", "output": []any{call}}))
					if err != nil {
						t.Fatal(err)
					}
					var envelope struct {
						Output []map[string]json.RawMessage `json:"output"`
					}
					if err := json.Unmarshal(response, &envelope); err != nil {
						t.Fatal(err)
					}
					output = envelope.Output
				}
				want := 1
				if tc.text != "" {
					want++
				}
				if len(output) != want {
					t.Fatalf("output = %s", mustTestJSON(t, output))
				}
				if tc.text != "" && commentaryText(t, output[0]) != "Journal update `/root` (`j1`)\n"+tc.text {
					t.Fatalf("authored text lost: %s", mustTestJSON(t, output))
				}
				if jsonString(output[len(output)-1], "arguments") != "{}" {
					t.Fatalf("commentary argument not stripped: %s", mustTestJSON(t, output))
				}
				replay := &parsedResponsesRequest{fields: map[string]json.RawMessage{"input": mustTestJSON(t, output)}}
				if err := proxy.reconcileInputPrefix(replay, transform.historySessionID); err != nil {
					t.Fatal(err)
				}
				var restored []map[string]json.RawMessage
				if err := json.Unmarshal(replay.fields["input"], &restored); err != nil {
					t.Fatal(err)
				}
				if len(restored) != 1 || jsonString(restored[0], "arguments") != tc.arguments {
					t.Fatalf("replay changed: %s", replay.fields["input"])
				}
			})
		}
	}
}

func TestNoninstrumentedCommentaryToolsPassThrough(t *testing.T) {
	for _, tc := range []struct {
		name, tools, input, namespace, arguments string
	}{
		{"strict", `[{"type":"function","name":"lookup","strict":true,"parameters":{"type":"object"}}]`, `[]`, "", `null`},
		{"owned", `[{"type":"function","name":"lookup","parameters":{"type":"object","properties":{"journal":{"type":"boolean"}}}}]`, `[]`, "", ` { "journal" : true } `},
		{"provider", `[]`, `[{"type":"additional_tools","tools":[{"type":"namespace","name":"external","tools":[{"type":"function","name":"lookup","parameters":{"type":"array"}}]}]}]`, "external", `[1, 2]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fields := map[string]json.RawMessage{"tools": json.RawMessage(tc.tools), "input": json.RawMessage(tc.input)}
			catalog, err := prepareCommentaryTools(fields, decodeResponsesToolCatalog(fields))
			if err != nil {
				t.Fatal(err)
			}
			transform, _, _, _ := newMekugiTestTransform(t, testTranslator(t, new(int)))
			transform.commentaryTools = catalog
			call := map[string]any{"type": "function_call", "id": "item", "call_id": "call", "name": "lookup", "namespace": tc.namespace, "arguments": tc.arguments}
			for _, event := range []map[string]any{
				{"type": "response.output_item.added", "item": call},
				{"type": "response.function_call_arguments.delta", "item_id": "item", "delta": tc.arguments},
				{"type": "response.output_item.done", "item": call},
			} {
				raw := mustTestJSON(t, event)
				got, err := transform.TransformSSE(raw)
				if err != nil || len(got) != 1 || string(got[0]) != string(raw) {
					t.Fatalf("SSE passthrough changed: %s, %v", got, err)
				}
			}
			got, err := transform.TransformJSON(mustTestJSON(t, map[string]any{"status": "completed", "output": []any{call}}))
			if err != nil {
				t.Fatal(err)
			}
			var response struct {
				Output []map[string]json.RawMessage `json:"output"`
			}
			if err := json.Unmarshal(got, &response); err != nil {
				t.Fatal(err)
			}
			if len(response.Output) != 1 || jsonString(response.Output[0], "arguments") != tc.arguments {
				t.Fatalf("JSON passthrough changed: %s", got)
			}
			if len(transform.local) != 0 {
				t.Fatal("passthrough calls consumed replay retention")
			}
		})
	}
}
