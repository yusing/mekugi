package responses

import (
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	json "encoding/json/v2"
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
