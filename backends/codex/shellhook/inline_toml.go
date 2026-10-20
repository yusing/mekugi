package shellhook

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"maps"
	"slices"
	"strings"
)

// inlineTOML converts host-returned hook values into a CLI override without
// coercing numbers through float64. JSON strings are normalized to avoid TOML's
// rejection of JSON surrogate escapes. TOML configuration contains no nulls.
func inlineTOML(data jsontext.Value) (string, error) {
	var parts []string
	switch data.Kind() {
	case '{':
		var fields map[string]jsontext.Value
		if err := json.Unmarshal(data, &fields); err != nil {
			return "", err
		}
		for _, key := range slices.Sorted(maps.Keys(fields)) {
			name, err := quoteTOML(key)
			if err != nil {
				return "", err
			}
			value, err := inlineTOML(fields[key])
			if err != nil {
				return "", err
			}
			parts = append(parts, name+"="+value)
		}
		return "{" + strings.Join(parts, ",") + "}", nil
	case '[':
		var values []jsontext.Value
		if err := json.Unmarshal(data, &values); err != nil {
			return "", err
		}
		for _, value := range values {
			part, err := inlineTOML(value)
			if err != nil {
				return "", err
			}
			parts = append(parts, part)
		}
		return "[" + strings.Join(parts, ",") + "]", nil
	case '"':
		var value string
		if err := json.Unmarshal(data, &value); err != nil {
			return "", err
		}
		return quoteTOML(value)
	case '0', 't', 'f':
		return string(data), nil
	default:
		return "", errors.New("hook configuration contains a value unavailable in TOML")
	}
}

// JSON and TOML basic strings share escaping, except TOML also forbids raw DEL.
func quoteTOML(value string) (string, error) {
	encoded, err := json.Marshal(&value)
	return strings.ReplaceAll(string(encoded), "\x7f", `\u007f`), err
}
