// Source: internal/router/responses_input.go:11:57@[7a069269] raw input views.
package responses

import (
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
)

// Input exposes text and arrays of raw items. Other valid shapes have neither.
type Input struct {
	Text  *string
	Items []json.RawMessage
}

// DecodeInput decodes a Responses input field into its structured form.
func DecodeInput(raw json.RawMessage) (Input, error) {
	if text, ok := DecodeString(raw); ok {
		return Input{Text: new(text)}, nil
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) == nil {
		return Input{Items: items}, nil
	}
	if !jsontext.Value(raw).IsValid(jsontext.AllowDuplicateNames(true), jsontext.AllowInvalidUTF8(true)) {
		return Input{}, errors.New("invalid JSON in Responses input")
	}
	return Input{}, nil
}
