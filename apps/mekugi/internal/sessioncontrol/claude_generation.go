package sessioncontrol

import "encoding/json/jsontext"

// Native SDK stream events carry the same admitted tool identity and partial
// input as Responses events. Both producers use the request-scoped owner.
type claudeGenerationEvent struct {
	Type  string `json:"type"`
	Index *int   `json:"index"`
	Block struct {
		Type  string         `json:"type"`
		ID    string         `json:"id"`
		Name  string         `json:"name"`
		Input jsontext.Value `json:"input"`
	} `json:"content_block"`
	Delta struct {
		Type string `json:"type"`
		JSON string `json:"partial_json"`
	} `json:"delta"`
}

func (r *ClaudeRuntime) observeToolGeneration(event claudeGenerationEvent) {
	key := GenerationItem{ID: event.Block.ID}
	if event.Index != nil {
		key.Index, key.Indexed = *event.Index, true
	}
	switch event.Type {
	case "message_start", "message_stop":
		r.generationInputs.EndGeneration(&r.generation)
	case "content_block_start":
		if event.Block.Type != "tool_use" && event.Block.Type != "server_tool_use" && event.Block.Type != "mcp_tool_use" {
			return
		}
		r.generationInputs.SetToolInput(&r.generation, r.ledger.Settings.Workspace, r.ledger.Info.Thread, key, true)
		initial := ""
		if len(event.Block.Input) > 0 && string(event.Block.Input) != "{}" {
			initial = string(event.Block.Input)
		}
		r.generationInputs.IdentifyToolInput(&r.generation, key, event.Block.Name, false, initial)
	case "content_block_delta":
		if event.Delta.Type == "input_json_delta" {
			r.generationInputs.IdentifyToolInput(&r.generation, key, "", false, event.Delta.JSON)
		}
	case "content_block_stop":
		r.generationInputs.SetToolInput(&r.generation, r.ledger.Settings.Workspace, r.ledger.Info.Thread, key, false)
	}
}
