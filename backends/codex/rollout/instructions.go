package rollout

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
)

// ReferencedInstructionItems materializes authenticated history only when a new
// branch has no locally retained instruction provenance. The prefix reader checks
// every source identity and the exact native ordinal/byte boundary.
func ReferencedInstructionItems(ctx context.Context, thread, path string, resolve PrefixResolver) ([]jsontext.Value, error) {
	reader := prefixReader{ctx: ctx, resolve: resolve, visiting: make(map[string]bool), remaining: maxInspectionBytes}
	_, rows, _, err := reader.read(PrefixSource{Thread: thread, Rollout: thread, Path: path}, nil, PrefixBoundary{})
	if err != nil {
		return nil, err
	}
	var result []jsontext.Value
	for _, row := range rows {
		if row.Type != "response_item" && row.Type != "compacted" {
			continue
		}
		payload, err := json.Marshal(row.Payload)
		if err != nil {
			return nil, err
		}
		result = append(result, instructionItems(row.Type, payload)...)
	}
	return result, nil
}

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
		result = append(result, instructionItems(record.Type, record.Payload)...)
	}
	return result
}

func instructionItems(kind string, payload jsontext.Value) []jsontext.Value {
	items := []jsontext.Value{payload}
	switch kind {
	case "response_item":
	case "compacted":
		var compact struct {
			Items []jsontext.Value `json:"replacement_history"`
		}
		if json.Unmarshal(payload, &compact) != nil {
			return nil
		}
		items = compact.Items
	default:
		return nil
	}
	var result []jsontext.Value
	for _, raw := range items {
		var item struct {
			Role     string         `json:"role"`
			Metadata jsontext.Value `json:"internal_chat_message_metadata_passthrough"`
		}
		if json.Unmarshal(raw, &item) == nil && item.Role == "developer" && item.Metadata.Kind() == '{' {
			result = append(result, raw)
		}
	}
	return result
}
