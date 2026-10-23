package rollout

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
)

// InstructionItems reads provenance-bearing developer messages from the bounded,
// identity-validated host tail. Older admitted parts remain with their feature.
func InstructionItems(thread, path string) []jsontext.Value {
	var result []jsontext.Value
	for line := range reverseRecords(thread, path) {
		var record struct {
			Type    string         `json:"type"`
			Payload jsontext.Value `json:"payload"`
		}
		if json.Unmarshal(line, &record) != nil {
			continue
		}
		items := []jsontext.Value{record.Payload}
		switch record.Type {
		case "response_item":
		case "compacted":
			var compact struct {
				Items []jsontext.Value `json:"replacement_history"`
			}
			if json.Unmarshal(record.Payload, &compact) != nil {
				continue
			}
			items = compact.Items
		default:
			continue
		}
		for _, raw := range items {
			var item struct {
				Role     string         `json:"role"`
				Metadata jsontext.Value `json:"internal_chat_message_metadata_passthrough"`
			}
			if json.Unmarshal(raw, &item) == nil && item.Role == "developer" && item.Metadata.Kind() == '{' {
				result = append(result, raw)
			}
		}
	}
	return result
}
