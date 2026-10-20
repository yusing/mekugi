package responses

import (
	"bytes"
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"maps"
	"slices"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

// Source: internal/router/request_wire.go:12:82@06fc40e4 byte-preserving projection.

// WireBody preserves the received envelope and untouched field values. Only
// fields changed by request projection are substituted; new fields are appended.
func (r Request) WireBody(fields map[string]jsontext.Value) ([]byte, error) {
	original := r.originalBody
	if len(original) == 0 {
		original = []byte("{}")
	}
	decoder := jsontext.NewDecoder(bytes.NewReader(original), jsonv1.DefaultOptionsV1())
	if _, err := decoder.ReadToken(); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	cursor := 0
	seen := make(map[string]bool)
	for decoder.PeekKind() != '}' {
		key, err := decoder.ReadToken()
		if err != nil {
			return nil, err
		}
		name := key.String()
		raw, err := decoder.ReadValue()
		if err != nil {
			return nil, err
		}
		end := int(decoder.InputOffset())
		start := end - len(raw)
		value, exists := fields[name]
		if !exists {
			return nil, fmt.Errorf("request projection removed field %q", name)
		}
		out.Write(original[cursor:start])
		// Compare against the decoded request's last occurrence for duplicate keys.
		// Unchanged duplicates stay byte-identical; changed duplicates all receive
		// the projected value, so no consumer can observe a stale occurrence.
		if responses.SameJSONValue(value, r.originalFields[name]) {
			out.Write(raw)
		} else {
			if !jsontext.Value(value).IsValid(jsonv1.DefaultOptionsV1()) {
				return nil, fmt.Errorf("invalid projected request field %q", name)
			}
			out.Write(value)
		}
		cursor = end
		seen[name] = true
	}
	for _, name := range slices.Sorted(maps.Keys(fields)) {
		if seen[name] {
			continue
		}
		value := fields[name]
		if !jsontext.Value(value).IsValid(jsonv1.DefaultOptionsV1()) {
			return nil, fmt.Errorf("invalid projected request field %q", name)
		}
		if len(seen) != 0 {
			out.WriteByte(',')
		}
		key, _ := json.Marshal(&name, jsonv1.DefaultOptionsV1())
		// For an empty object, preserve all whitespace before its closing brace.
		if cursor == 0 {
			if _, err := decoder.ReadToken(); err != nil {
				return nil, err
			}
			cursor = int(decoder.InputOffset()) - 1
			out.Write(original[:cursor])
		}
		out.Write(key)
		out.WriteByte(':')
		out.Write(value)
		seen[name] = true
	}
	out.Write(original[cursor:])
	return out.Bytes(), nil
}
