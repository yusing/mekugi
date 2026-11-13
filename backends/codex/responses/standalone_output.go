package responses

import (
	"bytes"
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

// StandaloneFunctionOutput is client-supplied context, not a correlated tool
// completion. Its shape grants no evidence or execution authority.
// Source: codex-rs/protocol/src/models.rs:1156,3402@0df4aa677 FunctionCallOutput.
func StandaloneFunctionOutput(item map[string]jsontext.Value) bool {
	call := bytes.TrimSpace(item["call_id"])
	return responses.StringField(item, "type") == "function_call_output" && responses.StringField(item, "name") != "" && (len(call) == 0 || bytes.Equal(call, []byte("null")))
}

// ProjectStandaloneOutputs adapts native context that has no correlated tool
// call to provider-compatible assistant messages. Run after native consumers;
// retained output shapes and identities remain authoritative locally.
func ProjectStandaloneOutputs(input []byte) ([]byte, error) {
	if !bytes.Contains(input, []byte("function_call_output")) && !bytes.Contains(input, []byte(`"source_acquisition"`)) {
		return input, nil
	}
	var items []jsontext.Value
	if err := json.Unmarshal(input, &items, jsonv1.DefaultOptionsV1()); err != nil {
		return nil, err
	}
	changed := false
	for i, raw := range items {
		if !bytes.Contains(raw, []byte("function_call_output")) && !bytes.Contains(raw, []byte(`"source_acquisition"`)) {
			continue
		}
		var item map[string]jsontext.Value
		if err := json.Unmarshal(raw, &item, jsonv1.DefaultOptionsV1()); err != nil {
			return nil, err
		}
		const metadataKey = "internal_chat_message_metadata_passthrough"
		var metadata map[string]jsontext.Value
		if json.Unmarshal(item[metadataKey], &metadata) == nil {
			if _, exists := metadata["source_acquisition"]; exists {
				delete(metadata, "source_acquisition")
				if len(metadata) == 0 {
					delete(item, metadataKey)
				} else {
					item[metadataKey] = responses.MustMarshalJSON(metadata)
				}
				items[i], changed = responses.MustMarshalJSON(item), true
			}
		}
		if !StandaloneFunctionOutput(item) {
			continue
		}
		name := responses.StringField(item, "name")
		if namespace := responses.StringField(item, "namespace"); namespace != "" {
			name = namespace + "." + name
		}
		parts := []map[string]jsontext.Value{{
			"type": responses.MustMarshalJSON("output_text"),
			"text": responses.MustMarshalJSON("Tool context: " + name),
		}}
		var text string
		if item["output"].Kind() == '"' && json.Unmarshal(item["output"], &text, jsonv1.DefaultOptionsV1()) == nil {
			parts = append(parts, map[string]jsontext.Value{"type": responses.MustMarshalJSON("output_text"), "text": responses.MustMarshalJSON(text)})
		} else {
			var content []map[string]jsontext.Value
			if item["output"].Kind() != '[' || json.Unmarshal(item["output"], &content, jsonv1.DefaultOptionsV1()) != nil {
				return nil, fmt.Errorf("unsupported standalone tool context output for %q", name)
			}
			for _, part := range content {
				switch responses.StringField(part, "type") {
				case "input_text", "output_text", "text":
					if part["text"].Kind() != '"' {
						return nil, fmt.Errorf("invalid standalone tool context text for %q", name)
					}
					part["type"] = responses.MustMarshalJSON("output_text")
				case "input_image":
					if responses.StringField(part, "image_url") == "" {
						return nil, fmt.Errorf("standalone tool context for %q requires an image URL", name)
					}
				case "refusal":
					if part["refusal"].Kind() != '"' {
						return nil, fmt.Errorf("invalid standalone tool context refusal for %q", name)
					}
				default:
					return nil, fmt.Errorf("unsupported standalone tool context content for %q", name)
				}
				parts = append(parts, part)
			}
		}
		projected := map[string]any{
			"type": "message", "role": "assistant", "content": parts,
		}
		if metadata := item[metadataKey]; len(metadata) != 0 {
			projected[metadataKey] = metadata
		}
		items[i] = responses.MustMarshalJSON(projected)
		changed = true
	}
	if !changed {
		return input, nil
	}
	return responses.MarshalJSON(items)
}
