package router

import (
	"encoding/json"
	"errors"
)

// responsesToolCatalog owns the request-local decode of every Responses tool
// definition. Consumers keep their own validation policy while sharing this
// tree, because malformed provider-owned sections do not have one universal
// failure behavior.
type responsesToolCatalog struct {
	top             *responsesToolSection
	additional      []*responsesAdditionalTools
	inputItems      []json.RawMessage
	inputObjectsErr error
}

type responsesAdditionalTools struct {
	itemIndex int
	item      map[string]json.RawMessage
	tools     *responsesToolSection
}

type responsesToolSection struct {
	present  bool
	array    bool
	raw      json.RawMessage
	rawTools []json.RawMessage
	tools    []*responsesToolDefinition
	err      error
}

// responsesToolDefinition exposes the stable Responses tool fields while its
// raw object remains authoritative for provider- and plugin-owned extensions.
type responsesToolDefinition struct {
	nested      *responsesToolSection
	fields      map[string]json.RawMessage
	Type        string
	Name        string
	Description string
	Title       string
}

// decodeResponsesToolDefinition unmarshals a Responses tool definition from raw JSON.
func decodeResponsesToolDefinition(raw json.RawMessage) (*responsesToolDefinition, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	if fields == nil {
		return &responsesToolDefinition{}, nil
	}
	return newResponsesToolDefinition(fields), nil
}

// newResponsesToolDefinition creates a responsesToolDefinition from decoded JSON fields.
func newResponsesToolDefinition(fields map[string]json.RawMessage) *responsesToolDefinition {
	tool := &responsesToolDefinition{
		fields:      fields,
		Type:        jsonString(fields, "type"),
		Name:        jsonString(fields, "name"),
		Description: jsonString(fields, "description"),
		Title:       jsonString(fields, "title"),
	}
	if nested, ok := fields["tools"]; ok {
		tool.nested = decodeResponsesToolSection(nested, true)
	}
	return tool
}

func (tool *responsesToolDefinition) MarshalJSON() ([]byte, error) {
	if tool == nil || tool.fields == nil {
		return []byte("null"), nil
	}
	return marshalProtocolJSON(tool.fields)
}

// setDescription updates the tool description in both the field and the underlying map.
func (tool *responsesToolDefinition) setDescription(description string) {
	tool.Description = description
	tool.fields["description"] = mustMarshalJSON(description)
}

// rawField returns the raw JSON for the named field.
func (tool *responsesToolDefinition) rawField(name string) json.RawMessage {
	if tool == nil || tool.fields == nil {
		return nil
	}
	return tool.fields[name]
}

// setRawField updates a raw JSON field in the tool definition.
func (tool *responsesToolDefinition) setRawField(name string, value json.RawMessage) {
	tool.fields[name] = value
}

// decodeResponsesToolCatalog decodes the tool catalog from a Responses request.
func decodeResponsesToolCatalog(fields map[string]json.RawMessage) *responsesToolCatalog {
	rawTop, topPresent := fields["tools"]
	catalog := &responsesToolCatalog{
		top: decodeResponsesToolSection(rawTop, topPresent),
	}

	rawInput, inputPresent := fields["input"]
	if !inputPresent {
		return catalog
	}
	if err := json.Unmarshal(rawInput, &catalog.inputItems); err != nil {
		catalog.inputObjectsErr = err
		return catalog
	}
	for itemIndex, rawItem := range catalog.inputItems {
		var item map[string]json.RawMessage
		if err := json.Unmarshal(rawItem, &item); err != nil {
			catalog.inputObjectsErr = errors.Join(catalog.inputObjectsErr, err)
			continue
		}
		if jsonString(item, "type") != "additional_tools" {
			continue
		}
		rawTools, toolsPresent := item["tools"]
		catalog.additional = append(catalog.additional, &responsesAdditionalTools{
			itemIndex: itemIndex,
			item:      item,
			tools:     decodeResponsesToolSection(rawTools, toolsPresent),
		})
	}
	return catalog
}

// decodeResponsesToolSection decodes a tool section from raw JSON.
func decodeResponsesToolSection(raw json.RawMessage, present bool) *responsesToolSection {
	section := &responsesToolSection{present: present, raw: raw}
	if !present {
		return section
	}
	var definitions []json.RawMessage
	if err := json.Unmarshal(raw, &definitions); err != nil {
		section.err = err
		return section
	}
	section.array = definitions != nil
	section.rawTools = definitions
	if definitions == nil {
		return section
	}
	section.tools = make([]*responsesToolDefinition, len(definitions))
	for index, definition := range definitions {
		tool, err := decodeResponsesToolDefinition(definition)
		if err != nil {
			section.err = errors.Join(section.err, err)
			continue
		}
		section.tools[index] = tool
	}
	return section
}

// appendTop appends tools to the top-level tool catalog.
func (c *responsesToolCatalog) appendTop(tools []*responsesToolDefinition) {
	c.top.present = true
	c.top.array = true
	c.top.tools = append(c.top.tools, tools...)
	for _, tool := range tools {
		c.top.rawTools = append(c.top.rawTools, mustMarshalJSON(tool))
	}
}

// removeTop removes a tool from the top-level catalog at the given index.
func (c *responsesToolCatalog) removeTop(index int) {
	c.top.tools = append(c.top.tools[:index], c.top.tools[index+1:]...)
	c.top.rawTools = append(c.top.rawTools[:index], c.top.rawTools[index+1:]...)
}

// encodeTop encodes the top-level tools back into the request fields.
func (c *responsesToolCatalog) encodeTop(fields map[string]json.RawMessage) error {
	encoded, err := marshalProtocolJSON(c.top.tools)
	if err != nil {
		return err
	}
	fields["tools"] = encoded
	return nil
}

// encodeAdditional encodes additional tools back into the request fields.
func (c *responsesToolCatalog) encodeAdditional(fields map[string]json.RawMessage, group *responsesAdditionalTools, section *responsesToolSection) error {
	if section != group.tools {
		for _, tool := range group.tools.tools {
			if tool == nil || tool.nested != section {
				continue
			}
			encoded, err := marshalProtocolJSON(section.tools)
			if err != nil {
				return err
			}
			tool.setRawField("tools", encoded)
			break
		}
	}
	encodedTools, err := marshalProtocolJSON(group.tools.tools)
	if err != nil {
		return err
	}
	group.item["tools"] = encodedTools
	encodedItem, err := marshalProtocolJSON(group.item)
	if err != nil {
		return err
	}
	c.inputItems[group.itemIndex] = encodedItem
	encodedInput, err := marshalProtocolJSON(c.inputItems)
	if err != nil {
		return err
	}
	fields["input"] = encodedInput
	return nil
}
