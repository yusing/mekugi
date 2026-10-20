// Source: apps/mekugi/internal/router/grok_catalog.go:22:119@[8877886e] catalog assembly.
package modelcatalog

import (
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"errors"
	"maps"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	"github.com/yusing/mekugi/providers/grok"
	"github.com/yusing/mekugi/providers/opencode"
)

// Selection is the invocation's enabled third-party providers and model namespace.
type Selection struct {
	GrokEnabled    bool
	GrokUnprefixed bool
	OpenCode       opencode.Config
}

// Build replaces the bundled model list with the selected third-party models,
// preserving native v2 instruction/executor metadata and unknown catalog fields.
func Build(body []byte, selection Selection) ([]byte, error) {
	// Preserve the bundled catalog's existing JSON compatibility and stable bytes.
	var catalog map[string]jsonv1.RawMessage
	if json.Unmarshal(body, &catalog, jsonv1.DefaultOptionsV1()) != nil || catalog == nil {
		return nil, errors.New("invalid Codex model catalog")
	}
	var models []map[string]jsonv1.RawMessage
	if json.Unmarshal(catalog["models"], &models, jsonv1.DefaultOptionsV1()) != nil {
		return nil, errors.New("codex model catalog is missing models")
	}
	var template map[string]jsonv1.RawMessage
	for _, model := range models {
		if responses.StringField(model, "slug") == "gpt-6-sol" && responses.StringField(model, "multi_agent_version") == "v2" {
			template = model
		}
	}
	if template == nil {
		for _, model := range models {
			slug := responses.StringField(model, "slug")
			if !grok.IsModel(slug) && !opencode.IsModel(slug) && responses.StringField(model, "multi_agent_version") == "v2" {
				template = model
				break
			}
		}
	}
	if template == nil {
		for _, model := range models {
			if responses.StringField(model, "multi_agent_version") == "v2" {
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
		"description":    "",
		"context_window": 500000, "max_context_window": 500000,
		"visibility": "list", "supported_in_api": true, "priority": 100, "default_reasoning_level": "high",
		"supported_reasoning_levels": []map[string]string{{"effort": "low", "description": "Low reasoning"}, {"effort": "medium", "description": "Medium reasoning"}, {"effort": "high", "description": "High reasoning"}, {"effort": "xhigh", "description": "Extra-high reasoning"}},
		"multi_agent_version":        "v2", "use_responses_lite": false, "supports_search_tool": false,
		"input_modalities": []string{"text", "image"}, "supports_image_detail_original": false,
		"additional_speed_tiers": []any{}, "service_tiers": []any{}, "upgrade": nil, "availability_nux": nil,
	} {
		model[key] = responses.MustMarshalJSON(value)
	}
	// Do not inherit account-gated OpenAI scheduling or model-upgrade defaults.
	delete(model, "multi_agent_reasoning_effort")
	models = nil
	if selection.GrokEnabled {
		for i, id := range grok.Models() {
			entry := maps.Clone(model)
			slug := "grok:" + id
			if selection.GrokUnprefixed {
				slug = id
			}
			entry["slug"] = responses.MustMarshalJSON(slug)
			entry["display_name"] = responses.MustMarshalJSON(slug)
			entry["priority"] = responses.MustMarshalJSON(100 + i)
			models = append(models, entry)
		}
	}
	for _, service := range selection.OpenCode.Services() {
		for _, definition := range service.Models() {
			entry := maps.Clone(model)
			levels := []map[string]string{}
			for _, effort := range definition.Efforts {
				levels = append(levels, map[string]string{"effort": effort, "description": effort + " reasoning"})
			}
			slug := service.Prefix + ":" + definition.ID
			for key, value := range map[string]any{
				"slug": slug, "display_name": slug,
				"description":    definition.Description,
				"context_window": definition.Context, "max_context_window": definition.Context,
				"default_reasoning_level": nil, "supported_reasoning_levels": levels,
				"input_modalities": definition.Modalities,
			} {
				entry[key] = responses.MustMarshalJSON(value)
			}
			if definition.Context == 0 {
				entry["context_window"] = jsonv1.RawMessage("null")
				entry["max_context_window"] = jsonv1.RawMessage("null")
			}
			models = append(models, entry)
		}
	}
	catalog["models"] = responses.MustMarshalJSON(models)
	return json.Marshal(&catalog, jsonv1.DefaultOptionsV1())
}
