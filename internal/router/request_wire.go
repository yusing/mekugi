package router

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"reflect"
	"slices"
)

// wireBody preserves the received envelope and untouched field values. Only
// fields changed by request projection are substituted; new fields are appended.
func (r parsedResponsesRequest) wireBody(fields map[string]json.RawMessage) ([]byte, error) {
	original := r.originalBody
	if len(original) == 0 {
		original = []byte("{}")
	}
	decoder := json.NewDecoder(bytes.NewReader(original))
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	cursor := 0
	seen := make(map[string]bool)
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		name := key.(string)
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
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
		if sameJSONValue(value, r.originalFields[name]) {
			out.Write(raw)
		} else {
			if !json.Valid(value) {
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
		if !json.Valid(value) {
			return nil, fmt.Errorf("invalid projected request field %q", name)
		}
		if len(seen) != 0 {
			out.WriteByte(',')
		}
		key, _ := json.Marshal(name)
		// For an empty object, emit its opening brace before the first new member.
		if cursor == 0 {
			out.Write(original[:int(decoder.InputOffset())])
			cursor = int(decoder.InputOffset())
		}
		out.Write(key)
		out.WriteByte(':')
		out.Write(value)
		seen[name] = true
	}
	out.Write(original[cursor:])
	return out.Bytes(), nil
}

// Projection may rebuild an unchanged field using a different JSON spelling.
// Preserve the incoming spelling whenever the decoded value is unchanged.
func sameJSONValue(left, right []byte) bool {
	if bytes.Equal(left, right) {
		return true
	}
	decode := func(raw []byte) (any, error) {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var value any
		err := decoder.Decode(&value)
		if err != nil {
			return nil, err
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			return nil, fmt.Errorf("invalid trailing projected JSON")
		}
		return value, nil
	}
	a, err := decode(left)
	if err != nil {
		return false
	}
	b, err := decode(right)
	return err == nil && reflect.DeepEqual(a, b)
}
