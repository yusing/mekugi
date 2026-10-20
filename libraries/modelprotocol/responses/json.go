// Source: internal/responses/json.go:1:29@[48bc03a0] protocol implementation.
package responses

import (
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
)

// MarshalJSON preserves the protocol and retained-record spelling: legacy JSON
// semantics, sorted keys, unescaped HTML, and no trailing newline.
func MarshalJSON(value any) ([]byte, error) {
	return json.Marshal(value, jsonv1.DefaultOptionsV1(), jsontext.EscapeForHTML(false))
}

// MustMarshalJSON encodes known JSON-compatible protocol values.
func MustMarshalJSON(value any) jsonv1.RawMessage {
	data, err := MarshalJSON(value)
	if err != nil {
		panic(err)
	}
	return data
}

// StringField returns a string field, or an empty string when it is unavailable.
func StringField(object map[string]jsonv1.RawMessage, name string) string {
	var value string
	_ = json.Unmarshal(object[name], &value, jsonv1.DefaultOptionsV1())
	return value
}

// Source: internal/router/mekugi_response_transform.go:436:443@[7a069269].
// ReplaceField preserves all other raw object fields.
func ReplaceField(payload []byte, name string, value jsonv1.RawMessage) ([]byte, error) {
	var object map[string]jsonv1.RawMessage
	if err := jsonv1.Unmarshal(payload, &object); err != nil || object == nil {
		return nil, errors.New("decode stream event")
	}
	object[name] = value
	return MarshalJSON(object)
}
