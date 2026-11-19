package responses

import (
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

// Source: internal/router/responses_tools.go:9:203@06fc40e4 request-local catalogs.

// ToolCatalog owns the request-local decode of every Responses tool
// definition. Consumers keep their own validation policy while sharing this
// tree, because malformed provider-owned sections do not have one universal
// failure behavior.
type ToolCatalog struct {
	Top        *ToolSection
	Additional []*ToolSection
	InputError error
}

type ToolSection struct {
	Present bool
	Array   bool

	Tools []*Tool
	Err   error
}

// Tool exposes the stable Responses tool fields for observation.
type Tool struct {
	Nested *ToolSection
	fields map[string]jsontext.Value
}

// decodeTool unmarshals a Responses tool definition from raw JSON.
func decodeTool(raw jsontext.Value) (*Tool, error) {
	var fields map[string]jsontext.Value
	if err := json.Unmarshal(raw, &fields, jsonv1.DefaultOptionsV1()); err != nil {
		return nil, err
	}
	if fields == nil {
		return &Tool{}, nil
	}
	return newTool(fields), nil
}

// newTool creates a Tool from decoded JSON fields.
func newTool(fields map[string]jsontext.Value) *Tool {
	tool := &Tool{
		fields: fields,
	}
	if nested, ok := fields["tools"]; ok {
		tool.Nested = decodeToolSection(nested, true)
	}
	return tool
}

// DecodeToolCatalog decodes the tool catalog from a Responses request.
func DecodeToolCatalog(fields map[string]jsontext.Value) *ToolCatalog {
	rawTop, topPresent := fields["tools"]
	catalog := &ToolCatalog{
		Top: decodeToolSection(rawTop, topPresent),
	}

	rawInput, inputPresent := fields["input"]
	if !inputPresent {
		return catalog
	}
	var items []jsontext.Value
	if err := json.Unmarshal(rawInput, &items, jsonv1.DefaultOptionsV1()); err != nil {
		catalog.InputError = err
		return catalog
	}
	for _, rawItem := range items {
		var item map[string]jsontext.Value
		if err := json.Unmarshal(rawItem, &item, jsonv1.DefaultOptionsV1()); err != nil {
			catalog.InputError = errors.Join(catalog.InputError, err)
			continue
		}
		if responses.StringField(item, "type") != "additional_tools" {
			continue
		}
		rawTools, toolsPresent := item["tools"]
		catalog.Additional = append(catalog.Additional, decodeToolSection(rawTools, toolsPresent))
	}
	return catalog
}

// decodeToolSection decodes a tool section from raw JSON.
func decodeToolSection(raw jsontext.Value, present bool) *ToolSection {
	section := &ToolSection{Present: present}
	if !present {
		return section
	}
	var definitions []jsontext.Value
	if err := json.Unmarshal(raw, &definitions, jsonv1.DefaultOptionsV1()); err != nil {
		section.Err = err
		return section
	}
	section.Array = definitions != nil
	if definitions == nil {
		return section
	}
	section.Tools = make([]*Tool, len(definitions))
	for index, definition := range definitions {
		tool, err := decodeTool(definition)
		if err != nil {
			section.Err = errors.Join(section.Err, err)
			continue
		}
		section.Tools[index] = tool
	}
	return section
}

func (tool *Tool) IsObject() bool      { return tool != nil && tool.fields != nil }
func (tool *Tool) Type() string        { return responses.StringField(tool.fields, "type") }
func (tool *Tool) Name() string        { return responses.StringField(tool.fields, "name") }
func (tool *Tool) Description() string { return responses.StringField(tool.fields, "description") }
