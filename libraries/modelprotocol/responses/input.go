// Source: internal/router/responses_input.go:11:57@[7a069269] raw input views.
package responses

import (
	"bytes"
	"encoding/json"
)

// Input keeps provider-owned JSON raw while exposing its two standard
// shapes to consumers.
type Input struct {
	raw   json.RawMessage
	Text  *string
	Items []json.RawMessage
	array bool
}

// DecodeInput decodes a Responses input field into its structured form.
func DecodeInput(raw json.RawMessage) (Input, error) {
	input := Input{raw: bytes.Clone(raw)}
	if text, ok := DecodeString(raw); ok {
		input.Text = new(text)
		return input, nil
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) == nil {
		input.Items = items
		input.array = true
		return input, nil
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return Input{}, err
	}
	return input, nil
}

// Encode marshals the Input back to JSON.
func (input Input) Encode() (json.RawMessage, error) {
	if input.Text != nil {
		return MarshalJSON(*input.Text)
	}
	if input.array {
		return MarshalJSON(input.Items)
	}
	return bytes.Clone(input.raw), nil
}
