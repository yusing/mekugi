package responses

import (
	"bytes"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"testing"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

func TestProjectStandaloneOutputs(t *testing.T) {
	for _, namespace := range []string{"threadcontrol", "other", ""} {
		for _, name := range []string{"spawn_agent", "followup_task", "wait_agent", "compact", "completion_guard", "unknown"} {
			for _, call := range []jsontext.Value{nil, jsontext.Value(`null`)} {
				item := map[string]jsontext.Value{
					"type":      responses.MustMarshalJSON("function_call_output"),
					"id":        responses.MustMarshalJSON("local-only"),
					"namespace": responses.MustMarshalJSON(namespace), "name": responses.MustMarshalJSON(name),
					"output": responses.MustMarshalJSON("Payload exactly as supplied: <>& /root"),
				}
				if call != nil {
					item["call_id"] = call
				}
				correlated := jsontext.Value(`{ "type": "function_call_output", "call_id": "real", "output": "unchanged" }`)
				input := responses.MustMarshalJSON([]jsontext.Value{correlated, responses.MustMarshalJSON(item), correlated})
				before := bytes.Clone(input)
				projected, err := ProjectStandaloneOutputs(input)
				if err != nil {
					t.Fatal(err)
				}
				var items []jsontext.Value
				if err := json.Unmarshal(projected, &items); err != nil || len(items) != 3 || !responses.SameJSONValue(items[0], correlated) || !responses.SameJSONValue(items[2], correlated) {
					t.Fatalf("neighboring correlated outputs changed: %s, %v", projected, err)
				}
				var message struct {
					Type    string                      `json:"type"`
					Role    string                      `json:"role"`
					Content []map[string]jsontext.Value `json:"content"`
				}
				if err := json.Unmarshal(items[1], &message); err != nil || message.Type != "message" || message.Role != "assistant" || len(message.Content) != 2 {
					t.Fatalf("standalone output is not non-human context: %s, %v", items[1], err)
				}
				label := name
				if namespace != "" {
					label = namespace + "." + name
				}
				if responses.StringField(message.Content[0], "text") != "Tool context: "+label ||
					responses.StringField(message.Content[1], "text") != responses.StringField(item, "output") || !bytes.Equal(input, before) {
					t.Fatal("attribution, payload or native input changed")
				}
				again, err := ProjectStandaloneOutputs(projected)
				if err != nil || !bytes.Equal(again, projected) {
					t.Fatal("projection is not idempotent")
				}
			}
		}
	}
}

func TestStandaloneContextReachesTranslatedEndpoints(t *testing.T) {
	input, err := ProjectStandaloneOutputs([]byte(`[{"type":"function_call_output","name":"spawn_agent","namespace":"threadcontrol","output":[{"type":"input_text","text":"Assignment retained."}]}]`))
	if err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"chat", "responses", "anthropic"} {
		tr, err := testTranslateFormat(responses.MustMarshalJSON(map[string]any{
			"model": "opencode-go:test", "input": jsontext.Value(input),
		}), format)
		if err != nil {
			t.Fatalf("%s standalone context: %v", format, err)
		}
		wire := responses.MustMarshalJSON(tr.body)
		for _, text := range []string{"Assignment retained.", "threadcontrol.spawn_agent", "assistant"} {
			if !bytes.Contains(wire, []byte(text)) {
				t.Fatalf("%s dropped standalone context %q: %s", format, text, wire)
			}
		}
	}
	image, err := ProjectStandaloneOutputs([]byte(`[{"type":"function_call_output","name":"context","output":[{"type":"input_image","image_url":"data:image/png;base64,AA=="}]}]`))
	if err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"chat", "responses", "anthropic"} {
		tr, err := testTranslateFormat(responses.MustMarshalJSON(map[string]any{
			"model": "opencode-go:test", "input": jsontext.Value(image),
		}), format)
		if format == "anthropic" {
			// Messages cannot represent images as assistant context. Retain its
			// explicit rejection rather than dropping media or claiming a user sent it.
			if err == nil {
				t.Fatal("Messages accepted unsupported assistant image context")
			}
			continue
		}
		if err != nil || !bytes.Contains(responses.MustMarshalJSON(tr.body), []byte("AA==")) {
			t.Fatalf("%s lost standalone image context: %v", format, err)
		}
	}
}

func TestStandaloneOutputContentAndUnrelatedHistory(t *testing.T) {
	for _, raw := range []string{
		`[{"type":"function_call_output","call_id":"c","name":"spawn_agent","output":"real result"}]`,
		`[{"type":"function_call_output","call_id":"","name":"spawn_agent","output":"empty call id"}]`,
		`[{"type":"function_call_output","output":"unnamed"}]`,
		`[{"type":"custom_tool_call_output","name":"exec","output":"not standalone"}]`,
	} {
		projected, err := ProjectStandaloneOutputs([]byte(raw))
		if err != nil || string(projected) != raw {
			t.Fatalf("unrelated history changed: %s, %v", projected, err)
		}
	}
	for _, output := range []string{
		`[]`,
		`[{"type":"input_text","text":"one"},{"type":"output_text","text":"two"},{"type":"text","text":"three"},{"type":"input_image","image_url":"data:image/png;base64,AA==","detail":"high"},{"type":"refusal","refusal":"no"}]`,
	} {
		input := []byte(`[{"type":"function_call_output","name":"context","output":` + output + `}]`)
		projected, err := ProjectStandaloneOutputs(input)
		if err != nil {
			t.Fatal(err)
		}
		var items []struct {
			Content []map[string]jsontext.Value `json:"content"`
		}
		if err := json.Unmarshal(projected, &items); err != nil {
			t.Fatal(err)
		}
		var parts []map[string]jsontext.Value
		if err := json.Unmarshal([]byte(output), &parts); err != nil {
			t.Fatal(err)
		}
		for _, part := range parts {
			if typ := responses.StringField(part, "type"); typ == "input_text" || typ == "text" {
				part["type"] = responses.MustMarshalJSON("output_text")
			}
		}
		if !responses.SameJSONValue(responses.MustMarshalJSON(items[0].Content[1:]), responses.MustMarshalJSON(parts)) {
			t.Fatalf("multipart output changed: %s", projected)
		}
	}
	for _, output := range []string{`null`, `{}`, `1`, `[{"type":"input_text","text":null}]`, `[{"type":"input_image","file_id":"opaque"}]`, `[{"type":"encrypted_content"}]`, `[{"type":"other"}]`} {
		input := []byte(`[{"type":"function_call_output","name":"context","output":` + output + `}]`)
		before := bytes.Clone(input)
		if projected, err := ProjectStandaloneOutputs(input); err == nil || projected != nil || !bytes.Equal(before, input) {
			t.Fatal("unsupported output accepted, exposed, or mutated native history")
		}
	}
}
