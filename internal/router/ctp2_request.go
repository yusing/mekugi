package router

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/tiktoken-go/tokenizer"
)

func newCTP2Codec() (*ctp2Codec, error) {
	tokens, err := tokenizer.ForModel(tokenizer.GPT5)
	if err != nil {
		return nil, fmt.Errorf("load GPT-5 tokenizer for CTP/2: %w", err)
	}
	return &ctp2Codec{tokens: tokens}, nil
}

func (c *ctp2Codec) prepareRequest(request *parsedResponsesRequest, nativeBody []byte) (*ctp2ResponseTransform, []byte, error) {
	if c == nil {
		return nil, nil, nil
	}
	transformed := maps.Clone(request.fields)
	view, err := decodeCTP2RequestView(transformed, request.responseTools())
	if err != nil {
		return nil, nativeBody, nil
	}
	if view.carrier == ctp2CarrierNone {
		return nil, nativeBody, nil
	}

	visible := newCTP2VisibleLineEncoder(c)
	encodeLocal := func(value string) string {
		encoded, _, encodeErr := c.encodeContentLocalString(value)
		if encodeErr != nil {
			err = errors.Join(err, encodeErr)
			return value
		}
		return encoded
	}
	if view.hasInput {
		if view.input.text != nil {
			*view.input.text = encodeLocal(*view.input.text)
		} else {
			if view.input.array {
				for _, group := range view.catalog.additional {
					projected, projectErr := projectCTP2AdditionalTools(group, encodeLocal)
					if projectErr != nil {
						err = errors.Join(err, projectErr)
						continue
					}
					view.input.items[group.itemIndex] = projected
				}
			}
			var preserveDeveloper func(string) string
			if view.carrier == ctp2CarrierDeveloperMessage {
				preserveDeveloper = func(value string) string { return value }
			}
			found, transformErr := transformCTP2Input(&view.input, encodeLocal, preserveDeveloper, visible, request.model() == grokModel)
			err = errors.Join(err, transformErr)
			if preserveDeveloper != nil && !found {
				return nil, nativeBody, nil
			}
		}
		if err == nil {
			transformed["input"], err = view.input.encode()
		}
	}
	if view.hasTools {
		projected, projectErr := projectCTP2ToolSection(view.catalog.top, encodeLocal)
		if projectErr != nil {
			err = errors.Join(err, projectErr)
		} else {
			transformed["tools"] = projected
		}
	}
	if err != nil {
		return nil, nativeBody, nil
	}

	compactBody, err := request.wireBody(transformed)
	if err != nil {
		return nil, nativeBody, nil
	}
	request.fields = transformed
	return &ctp2ResponseTransform{
		sources: cloneCTP2VisibleLineSources(visible.sources),
	}, compactBody, nil
}

func (c *ctp2Codec) count(value []byte) (int, error) {
	count, err := c.tokens.Count(string(value))
	if err != nil {
		return 0, fmt.Errorf("estimate CTP/2 tokens: %w", err)
	}
	if count < 0 {
		return 0, errors.New("estimate CTP/2 tokens: negative count")
	}
	return count, nil
}

// decodeCTP2RequestView decodes and analyzes a CTP/2 request's input and tool catalog.
func decodeCTP2RequestView(fields map[string]json.RawMessage, catalog *responsesToolCatalog) (ctp2RequestView, error) {
	view := ctp2RequestView{catalog: catalog}
	var instructions string
	if json.Unmarshal(fields["instructions"], &instructions) == nil && strings.TrimSpace(instructions) != "" {
		view.carrier = ctp2CarrierTopLevel
	}
	if raw, ok := fields["input"]; ok {
		if catalog.inputItems != nil {
			view.input = responsesInput{
				raw:   bytes.Clone(raw),
				items: slices.Clone(catalog.inputItems),
				array: true,
			}
		} else {
			input, err := decodeResponsesInput(raw)
			if err != nil {
				return ctp2RequestView{}, fmt.Errorf("decode CTP/2 input: %w", err)
			}
			view.input = input
		}
		view.hasInput = true
		if view.carrier == ctp2CarrierNone {
			found, err := transformFirstDeveloperText(&view.input, func(value string) string { return value })
			if err != nil {
				return ctp2RequestView{}, err
			}
			if found {
				view.carrier = ctp2CarrierDeveloperMessage
			}
		}
	}
	if view.carrier == ctp2CarrierNone {
		return view, nil
	}
	if _, ok := fields["tools"]; ok {
		view.hasTools = true
	}
	return view, nil
}

// decodeResponsesInput decodes a Responses input field into its structured form.
func decodeResponsesInput(raw json.RawMessage) (responsesInput, error) {
	input := responsesInput{raw: bytes.Clone(raw)}
	if text, ok := decodeJSONString(raw); ok {
		input.text = new(text)
		return input, nil
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) == nil {
		input.items = items
		input.array = true
		return input, nil
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return responsesInput{}, err
	}
	return input, nil
}

// encode marshals the responsesInput back to JSON.
func (input responsesInput) encode() (json.RawMessage, error) {
	if input.text != nil {
		return marshalProtocolJSON(*input.text)
	}
	if input.array {
		return marshalProtocolJSON(input.items)
	}
	return bytes.Clone(input.raw), nil
}

// transformCTP2Input applies transformations to a Responses input array.
func transformCTP2Input(
	input *responsesInput,
	transformString, transformFirstDeveloper func(string) string,
	visible *ctp2VisibleLineEncoder,
	preserveNativeArguments bool,
) (bool, error) {
	if input == nil || !input.array {
		return false, nil
	}
	developerTransformed := false
	for index, raw := range input.items {
		item, ok := decodeResponsesItem(raw)
		if !ok {
			continue
		}
		switch item.Type {
		case "message":
			if !developerTransformed && item.Role == "developer" {
				content, found, err := transformCTP2DeveloperContent(item.Content, transformString, transformFirstDeveloper)
				if err != nil {
					return false, err
				}
				if found {
					developerTransformed = true
					item.setContent(content)
					input.items[index] = mustMarshalJSON(item)
					continue
				}
			}
			content, changed, err := transformCTP2Content(item.Content, transformString, isCTP2TextPart)
			if err != nil {
				return false, err
			}
			if changed {
				item.setContent(content)
			}
		case "custom_tool_call":
			if item.Input != nil {
				item.setInput(transformString(*item.Input))
			}
		case "custom_tool_call_output", "function_call_output":
			output, changed, err := transformCTP2VisibleLineOutput(item.Output, item.CallID, visible)
			if err != nil {
				return false, err
			}
			if changed {
				item.setOutput(output)
			}
		case "function_call":
			// Chat function arguments must remain JSON, not a CTP text envelope.
			if item.Arguments != nil && !preserveNativeArguments {
				item.setArguments(transformString(*item.Arguments))
			}
		}
		input.items[index] = mustMarshalJSON(item)
	}
	return developerTransformed, nil
}

// transformCTP2DeveloperContent keeps the selected final text part native as the
// instruction carrier while allowing independent sibling parts to use CTP/2.
func transformCTP2DeveloperContent(
	raw json.RawMessage,
	transformOther, transformCarrier func(string) string,
) (json.RawMessage, bool, error) {
	if transformCarrier == nil {
		return raw, false, nil
	}
	if text, ok := decodeJSONString(raw); ok {
		return mustMarshalJSON(transformCarrier(text)), true, nil
	}
	parts, ok := decodeResponsesTextParts(raw)
	if !ok {
		return raw, false, nil
	}
	carrier := -1
	for index, part := range slices.Backward(parts) {
		if part.text != nil && isCTP2InputTextPart(part.typeName) {
			carrier = index
			break
		}
	}
	if carrier < 0 {
		return raw, false, nil
	}
	for index := range parts {
		part := &parts[index]
		if part.text == nil || !isCTP2InputTextPart(part.typeName) {
			continue
		}
		transformed := transformOther(*part.text)
		if index == carrier {
			transformed = transformCarrier(*part.text)
		}
		updated, err := replaceRawField(part.raw, "text", mustMarshalJSON(transformed))
		if err != nil {
			return nil, false, err
		}
		part.raw = updated
	}
	encoded, err := encodeResponsesTextParts(parts)
	return encoded, true, err
}

// transformFirstDeveloperText finds and transforms the first developer message's text content.
func transformFirstDeveloperText(input *responsesInput, transform func(string) string) (bool, error) {
	if input == nil || !input.array {
		return false, nil
	}
	for index, raw := range input.items {
		item, ok := decodeResponsesItem(raw)
		if !ok || item.Type != "message" || item.Role != "developer" {
			continue
		}
		content, found, err := transformLastTextContent(item.Content, transform)
		if err != nil {
			return false, err
		}
		if found {
			item.setContent(content)
			input.items[index] = mustMarshalJSON(item)
			return true, nil
		}
	}
	return false, nil
}

// transformLastTextContent finds and transforms the last text content part.
func transformLastTextContent(raw json.RawMessage, transform func(string) string) (json.RawMessage, bool, error) {
	if transform == nil {
		return raw, false, nil
	}
	if text, ok := decodeJSONString(raw); ok {
		return mustMarshalJSON(transform(text)), true, nil
	}
	parts, ok := decodeResponsesTextParts(raw)
	if !ok {
		return raw, false, nil
	}
	for index := len(parts) - 1; index >= 0; index-- {
		part := &parts[index]
		if part.text != nil && isCTP2InputTextPart(part.typeName) {
			updated, err := replaceRawField(part.raw, "text", mustMarshalJSON(transform(*part.text)))
			if err != nil {
				return nil, false, err
			}
			part.raw = updated
			encoded, err := encodeResponsesTextParts(parts)
			return encoded, true, err
		}
	}
	return raw, false, nil
}

// transformCTP2Content transforms text parts in message content.
func transformCTP2Content(
	raw json.RawMessage,
	transform func(string) string,
	isTextPart func(string) bool,
) (json.RawMessage, bool, error) {
	if text, ok := decodeJSONString(raw); ok {
		return mustMarshalJSON(transform(text)), true, nil
	}
	parts, ok := decodeResponsesTextParts(raw)
	if !ok {
		return raw, false, nil
	}
	changed := false
	for index := range parts {
		part := &parts[index]
		if part.text == nil || !isTextPart(part.typeName) {
			continue
		}
		updated, err := replaceRawField(part.raw, "text", mustMarshalJSON(transform(*part.text)))
		if err != nil {
			return nil, false, err
		}
		part.raw = updated
		changed = true
	}
	if !changed {
		return raw, false, nil
	}
	encoded, err := encodeResponsesTextParts(parts)
	return encoded, true, err
}

// decodeResponsesTextParts decodes message content into text parts.
func decodeResponsesTextParts(raw json.RawMessage) ([]responsesTextPart, bool) {
	var values []json.RawMessage
	if json.Unmarshal(raw, &values) != nil || values == nil {
		return nil, false
	}
	parts := make([]responsesTextPart, len(values))
	for index, value := range values {
		var fields struct {
			Type string  `json:"type"`
			Text *string `json:"text"`
		}
		_ = json.Unmarshal(value, &fields)
		parts[index] = responsesTextPart{raw: value, typeName: fields.Type, text: fields.Text}
	}
	return parts, true
}

// encodeResponsesTextParts encodes text parts back to JSON.
func encodeResponsesTextParts(parts []responsesTextPart) (json.RawMessage, error) {
	values := make([]json.RawMessage, len(parts))
	for index := range parts {
		values[index] = parts[index].raw
	}
	return marshalProtocolJSON(values)
}

// isCTP2TextPart reports whether a type name is any CTP/2 text part.
func isCTP2TextPart(typeName string) bool {
	return typeName == "input_text" || typeName == "output_text" || typeName == "text"
}

// isCTP2InputTextPart reports whether a type name is an input text part.
func isCTP2InputTextPart(typeName string) bool {
	return typeName == "input_text" || typeName == "text"
}

// isCTP2AssistantTextPart reports whether a type name is an assistant text part.
func isCTP2AssistantTextPart(typeName string) bool {
	return typeName == "output_text" || typeName == "text"
}

// projectCTP2AdditionalTools transforms an additional_tools item for CTP/2.
func projectCTP2AdditionalTools(group *responsesAdditionalTools, transform func(string) string) (json.RawMessage, error) {
	item := maps.Clone(group.item)
	if !group.tools.present {
		return marshalProtocolJSON(item)
	}
	tools, err := projectCTP2ToolSection(group.tools, transform)
	if err != nil {
		return nil, err
	}
	item["tools"] = tools
	return marshalProtocolJSON(item)
}

// projectCTP2ToolSection transforms a tool section for CTP/2.
func projectCTP2ToolSection(section *responsesToolSection, transform func(string) string) (json.RawMessage, error) {
	if section == nil || !section.array {
		return sectionRawTools(section), nil
	}
	definitions := slices.Clone(section.rawTools)
	for index, definition := range section.tools {
		if definition == nil || definition.fields == nil {
			continue
		}
		tool := maps.Clone(definition.fields)
		var description *string
		if json.Unmarshal(definition.rawField("description"), &description) == nil && description != nil {
			tool["description"] = mustMarshalJSON(transform(*description))
		}
		if definition.nested != nil {
			nested, err := projectCTP2ToolSection(definition.nested, transform)
			if err != nil {
				return nil, err
			}
			tool["tools"] = nested
		}
		encoded, err := marshalProtocolJSON(tool)
		if err != nil {
			return nil, err
		}
		definitions[index] = encoded
	}
	return marshalProtocolJSON(definitions)
}

// sectionRawTools returns the raw tools JSON from a section.
func sectionRawTools(section *responsesToolSection) json.RawMessage {
	if section == nil {
		return nil
	}
	return section.raw
}

func transformCTP2VisibleLineOutput(
	raw json.RawMessage,
	callID string,
	encoder *ctp2VisibleLineEncoder,
) (json.RawMessage, bool, error) {
	encode := func(locator, value string) (string, error) {
		encoded, err := encoder.encodeString(locator, value)
		if err != nil {
			return "", err
		}
		return encoded, nil
	}
	if text, ok := decodeJSONString(raw); ok {
		encoded, err := encode(callID, text)
		if err != nil {
			return nil, false, err
		}
		return mustMarshalJSON(encoded), true, nil
	}
	parts, ok := decodeResponsesTextParts(raw)
	if !ok {
		return raw, false, nil
	}
	changed := false
	for index := range parts {
		part := &parts[index]
		if part.text == nil || !isCTP2TextPart(part.typeName) {
			continue
		}
		locator := callID
		if locator != "" {
			locator += "/" + ctp2Base36(index)
		}
		encoded, err := encode(locator, *part.text)
		if err != nil {
			return nil, false, err
		}
		updated, err := replaceRawField(part.raw, "text", mustMarshalJSON(encoded))
		if err != nil {
			return nil, false, err
		}
		part.raw = updated
		changed = true
	}
	if !changed {
		return raw, false, nil
	}
	encoded, err := encodeResponsesTextParts(parts)
	return encoded, true, err
}
