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
	Additional []*AdditionalTools
	inputItems []jsontext.Value
	InputError error
}

type AdditionalTools struct {
	itemIndex int
	item      map[string]jsontext.Value
	Tools     *ToolSection
}

type ToolSection struct {
	Present bool
	Array   bool

	Tools []*Tool
	Err   error
}

// Tool exposes the stable Responses tool fields while its
// raw object remains authoritative for unknown extensions.
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

func (tool *Tool) MarshalJSON() ([]byte, error) {
	if tool == nil || tool.fields == nil {
		return []byte("null"), nil
	}
	return responses.MarshalJSON(tool.fields)
}

// setDescription updates the tool description in both the field and the underlying map.
func (tool *Tool) SetDescription(description string) {
	tool.fields["description"] = responses.MustMarshalJSON(description)
}

// rawField returns the raw JSON for the named field.
func (tool *Tool) RawField(name string) jsontext.Value {
	if tool == nil || tool.fields == nil {
		return nil
	}
	return tool.fields[name]
}

// setRawField updates a raw JSON field in the tool definition.
func (tool *Tool) SetRawField(name string, value jsontext.Value) {
	tool.fields[name] = value
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
	if err := json.Unmarshal(rawInput, &catalog.inputItems, jsonv1.DefaultOptionsV1()); err != nil {
		catalog.InputError = err
		return catalog
	}
	for itemIndex, rawItem := range catalog.inputItems {
		var item map[string]jsontext.Value
		if err := json.Unmarshal(rawItem, &item, jsonv1.DefaultOptionsV1()); err != nil {
			catalog.InputError = errors.Join(catalog.InputError, err)
			continue
		}
		if responses.StringField(item, "type") != "additional_tools" {
			continue
		}
		rawTools, toolsPresent := item["tools"]
		catalog.Additional = append(catalog.Additional, &AdditionalTools{
			itemIndex: itemIndex,
			item:      item,
			Tools:     decodeToolSection(rawTools, toolsPresent),
		})
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

// encodeTop encodes the top-level tools back into the request fields.
func (c *ToolCatalog) EncodeTop(fields map[string]jsontext.Value) error {
	encoded, err := responses.MarshalJSON(c.Top.Tools)
	if err != nil {
		return err
	}
	fields["tools"] = encoded
	return nil
}

// encodeAdditional encodes additional tools back into the request fields.
func (c *ToolCatalog) EncodeAdditional(fields map[string]jsontext.Value, group *AdditionalTools, section *ToolSection) error {
	if section != group.Tools {
		for _, tool := range group.Tools.Tools {
			if tool == nil || tool.Nested != section {
				continue
			}
			encoded, err := responses.MarshalJSON(section.Tools)
			if err != nil {
				return err
			}
			tool.SetRawField("tools", encoded)
			break
		}
	}
	encodedTools, err := responses.MarshalJSON(group.Tools.Tools)
	if err != nil {
		return err
	}
	group.item["tools"] = encodedTools
	encodedItem, err := responses.MarshalJSON(group.item)
	if err != nil {
		return err
	}
	c.inputItems[group.itemIndex] = encodedItem
	encodedInput, err := responses.MarshalJSON(c.inputItems)
	if err != nil {
		return err
	}
	fields["input"] = encodedInput
	return nil
}

func (tool *Tool) IsObject() bool      { return tool != nil && tool.fields != nil }
func (tool *Tool) Type() string        { return responses.StringField(tool.fields, "type") }
func (tool *Tool) Name() string        { return responses.StringField(tool.fields, "name") }
func (tool *Tool) Description() string { return responses.StringField(tool.fields, "description") }
