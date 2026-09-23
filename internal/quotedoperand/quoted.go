package quotedoperand

import (
	json "encoding/json/v2"
	"errors"
	"strings"
)

// DecodeQuoted decodes one JSON-compatible quoted operand and returns the
// unconsumed source. Literal horizontal tabs are normalized to the equivalent
// JSON escape before decoding; every other quoted-string validation rule is retained.
func DecodeQuoted(source string) (string, string, error) {
	source = strings.TrimLeft(source, " \t\r\n")
	if source == "" {
		var value string
		return "", "", json.Unmarshal(nil, &value)
	}
	if source[0] != '"' {
		return "", "", errors.New("quoted operand must begin with a double quote")
	}

	escaped := false
	for index := 1; index < len(source); index++ {
		switch {
		case escaped:
			escaped = false
		case source[index] == '\\':
			escaped = true
		case source[index] == '"':
			end := index + 1
			encoded := strings.ReplaceAll(source[:end], "\t", `\t`)
			var value string
			if err := json.Unmarshal([]byte(encoded), &value); err != nil {
				return "", "", err
			}
			return value, source[end:], nil
		}
	}

	encoded := strings.ReplaceAll(source, "\t", `\t`)
	var value string
	if err := json.Unmarshal([]byte(encoded), &value); err != nil {
		return "", "", err
	}
	return value, "", nil
}
