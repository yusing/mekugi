package router

import (
	"bytes"
	"encoding/json"
)

// responsesInput keeps provider-owned JSON raw while exposing its two standard
// shapes to the router.
type responsesInput struct {
	raw   json.RawMessage
	text  *string
	items []json.RawMessage
	array bool
}

type responsesTextPart struct {
	raw      json.RawMessage
	typeName string
	text     *string
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

// transformLastTextContent finds and transforms the last input text content part.
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
		if part.text != nil && isResponsesInputTextPart(part.typeName) {
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

// transformResponsesTextContent transforms selected text parts in message content.
func transformResponsesTextContent(
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

func encodeResponsesTextParts(parts []responsesTextPart) (json.RawMessage, error) {
	values := make([]json.RawMessage, len(parts))
	for index := range parts {
		values[index] = parts[index].raw
	}
	return marshalProtocolJSON(values)
}

func isResponsesInputTextPart(typeName string) bool {
	return typeName == "input_text" || typeName == "text"
}
