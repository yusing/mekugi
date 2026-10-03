package router

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"strings"
)

// Adapt only the schema carrier. Operations still validate in the same owner.
func (s *ObservationService) JournalSchema() jsontext.Value {
	if s.journal == nil {
		return nil
	}
	var schema map[string]any
	_ = json.Unmarshal(journalMutationsSchema(), &schema)
	delete(schema, "description") // Codex piggybacking guidance is not an MCP contract.
	items := schema["items"].(map[string]any)
	variants := items["anyOf"].([]any)
	var native []any
	for _, variant := range variants {
		v := variant.(map[string]any)
		properties := v["properties"].(map[string]any)
		op := properties["op"].(map[string]any)["enum"].([]any)[0]
		if op != "finish" {
			native = append(native, variant)
		}
	}
	items["anyOf"] = native
	raw, _ := json.Marshal(schema)
	// The shared schema normally lives under Codex's properties.journal. MCP's
	// Zod adapter consumes it as its own local document before embedding it.
	return jsontext.Value(strings.ReplaceAll(string(raw), "#/properties/journal/$defs/task", "#/$defs/task"))
}
