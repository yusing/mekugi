package router

import (
	"encoding/json"
	"errors"
	"maps"
	"slices"
)

// GrokModelCatalog builds a session catalog from Codex's selected native catalog.
// It rebuilds any cached Grok entry from a native v2 template, preserving the
// other models and Codex's evolving instruction and executor metadata.
func GrokModelCatalog(body []byte) ([]byte, error) {
	return ProviderModelCatalog(body, true, OpenCodeConfig{})
}

// ProviderModelCatalog adds only configured providers to the private catalog.
func ProviderModelCatalog(body []byte, grok bool, openCode OpenCodeConfig) ([]byte, error) {
	var catalog map[string]json.RawMessage
	if json.Unmarshal(body, &catalog) != nil || catalog == nil {
		return nil, errors.New("invalid Codex model catalog")
	}
	var models []map[string]json.RawMessage
	if json.Unmarshal(catalog["models"], &models) != nil {
		return nil, errors.New("Codex model catalog is missing models")
	}
	models = slices.DeleteFunc(models, func(model map[string]json.RawMessage) bool {
		return jsonString(model, "slug") == grokModel || isOpenCodeModel(jsonString(model, "slug"))
	})
	var template map[string]json.RawMessage
	for _, model := range models {
		if jsonString(model, "slug") == "gpt-5.6-sol" && jsonString(model, "multi_agent_version") == "v2" {
			template = model
		}
	}
	if template == nil {
		for _, model := range models {
			if jsonString(model, "multi_agent_version") == "v2" {
				template = model
				break
			}
		}
	}
	if template == nil {
		return nil, errors.New("third-party models require a Codex catalog with native v2 subagent support")
	}
	model := maps.Clone(template)
	for key, value := range map[string]any{
		"slug": grokModel, "display_name": grokModel, "description": "",
		"context_window": 500000, "max_context_window": 500000,
		"visibility": "list", "supported_in_api": true, "priority": 100, "default_reasoning_level": "high",
		"supported_reasoning_levels": []map[string]string{{"effort": "low", "description": "Low reasoning"}, {"effort": "medium", "description": "Medium reasoning"}, {"effort": "high", "description": "High reasoning"}, {"effort": "xhigh", "description": "Extra-high reasoning"}},
		"multi_agent_version":        "v2", "use_responses_lite": false, "supports_search_tool": false,
		"input_modalities": []string{"text", "image"}, "supports_image_detail_original": false,
		"additional_speed_tiers": []any{}, "service_tiers": []any{}, "upgrade": nil, "availability_nux": nil,
	} {
		model[key] = mustMarshalJSON(value)
	}
	// Do not inherit account-gated OpenAI scheduling or model-upgrade defaults.
	delete(model, "multi_agent_reasoning_effort")
	if grok {
		models = append(models, model)
	}
	for _, service := range openCode.services() {
		for _, definition := range service.models() {
			entry := maps.Clone(model)
			slug := service.prefix + ":" + definition.id
			for key, value := range map[string]any{
				"slug": slug, "display_name": slug,
				"description":    definition.Description,
				"context_window": definition.Context, "max_context_window": definition.Context,
				"default_reasoning_level": nil, "supported_reasoning_levels": definition.reasoningLevels(),
				"input_modalities": definition.Modalities,
			} {
				entry[key] = mustMarshalJSON(value)
			}
			if definition.Context == 0 {
				entry["context_window"] = json.RawMessage("null")
				entry["max_context_window"] = json.RawMessage("null")
			}
			models = append(models, entry)
		}
	}
	catalog["models"] = mustMarshalJSON(models)
	return json.Marshal(catalog)
}
