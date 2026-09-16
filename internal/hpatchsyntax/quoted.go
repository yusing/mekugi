package hpatchsyntax

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

// ValidOperandSpacing reports whether unquoted operands are separated by one
// ASCII space, as required by the compact-script grammar. Whitespace inside a
// quoted operand is not a separator.
func ValidOperandSpacing(source string) bool {
	quoted := false
	escaped := false
	for index := range len(source) {
		character := source[index]
		if quoted {
			switch {
			case escaped:
				escaped = false
			case character == '\\':
				escaped = true
			case character == '"':
				if index+1 < len(source) && source[index+1] != ' ' {
					return false
				}
				quoted = false
			}
			continue
		}
		if character == '"' {
			if index > 0 && source[index-1] != ' ' {
				return false
			}
			quoted = true
			continue
		}
		if character == '\t' || character == '\r' || character == '\n' ||
			character == ' ' && (index == 0 || index == len(source)-1 || source[index-1] == ' ') {
			return false
		}
	}
	return true
}
