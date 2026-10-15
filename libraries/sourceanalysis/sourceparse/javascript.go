package sourceparse

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
)

func JavaScriptValue(node *sitter.Node, source []byte) (any, bool) {
	if node == nil {
		return nil, false
	}
	switch node.Kind() {
	case "parenthesized_expression":
		if node.NamedChildCount() != 1 {
			return nil, false
		}
		return JavaScriptValue(node.NamedChild(0), source)
	case "object":
		value := make(map[string]any, node.NamedChildCount())
		for i := range node.NamedChildCount() {
			pair := node.NamedChild(uint(i))
			if pair.Kind() != "pair" {
				return nil, false
			}
			keyNode, valueNode := pair.ChildByFieldName("key"), pair.ChildByFieldName("value")
			if keyNode == nil || valueNode == nil {
				return nil, false
			}
			var key string
			switch keyNode.Kind() {
			case "property_identifier":
				key = keyNode.Utf8Text(source)
				if strings.ContainsRune(key, '\\') {
					return nil, false
				}
			case "string":
				var ok bool
				key, ok = JavaScriptString(keyNode.Utf8Text(source))
				if !ok {
					return nil, false
				}
			default:
				return nil, false
			}
			decoded, ok := JavaScriptValue(valueNode, source)
			if !ok {
				return nil, false
			}
			value[key] = decoded
		}
		return value, true
	case "array":
		value := make([]any, 0, node.NamedChildCount())
		cursor := int(node.StartByte()) + 1
		for i := range node.NamedChildCount() {
			child := node.NamedChild(uint(i))
			gap := strings.TrimSpace(string(source[cursor:int(child.StartByte())]))
			if i == 0 && gap != "" || i > 0 && gap != "," || child.Kind() == "comment" {
				return nil, false
			}
			decoded, ok := JavaScriptValue(child, source)
			if !ok {
				return nil, false
			}
			value = append(value, decoded)
			cursor = int(child.EndByte())
		}
		tail := strings.TrimSpace(string(source[cursor : int(node.EndByte())-1]))
		if len(value) == 0 && tail != "" || len(value) != 0 && tail != "" && tail != "," {
			return nil, false
		}
		return value, true
	case "string", "template_string":
		return JavaScriptString(node.Utf8Text(source))
	case "number":
		return jsonNumber(node.Utf8Text(source))
	case "true":
		return true, true
	case "false":
		return false, true
	case "null":
		return nil, true
	case "unary_expression":
		if node.NamedChildCount() != 1 || !strings.HasPrefix(strings.TrimSpace(node.Utf8Text(source)), "-") {
			return nil, false
		}
		child := node.NamedChild(0)
		if child.Kind() != "number" {
			return nil, false
		}
		return jsonNumber(node.Utf8Text(source))
	default:
		return nil, false
	}
}

func jsonNumber(source string) (any, bool) {
	if !json.Valid([]byte(source)) {
		return nil, false
	}
	value, err := strconv.ParseFloat(source, 64)
	if err != nil || math.IsInf(value, 0) {
		return nil, false
	}
	return value, true
}

func JavaScriptString(source string) (string, bool) {
	if len(source) < 2 || source[0] != source[len(source)-1] || source[0] != '"' && source[0] != '\'' && source[0] != '`' {
		return "", false
	}
	quote := source[0]
	rest := source[1 : len(source)-1]
	var value strings.Builder
	for rest != "" {
		if quote == '`' {
			if strings.HasPrefix(rest, "${") {
				return "", false // Never evaluate template interpolation.
			}
			if rest[0] == '\n' || rest[0] == '\r' {
				value.WriteByte('\n')
				if strings.HasPrefix(rest, "\r\n") {
					rest = rest[1:]
				}
				rest = rest[1:]
				continue
			}
		}
		if rest[0] == '\\' {
			if len(rest) < 2 {
				return "", false
			}
			switch rest[1] {
			case '`', '$':
				if quote != '`' {
					return "", false
				}
				value.WriteByte(rest[1])
				rest = rest[2:]
				continue
			case '\'', '"', '/', '\\':
				value.WriteByte(rest[1])
				rest = rest[2:]
				continue
			case 'b', 'f', 'n', 'r', 't', 'v', 'x', 'u':
			default:
				return "", false
			}
		}
		char, _, tail, err := strconv.UnquoteChar(rest, quote)
		if err != nil {
			return "", false
		}
		value.WriteRune(char)
		rest = tail
	}
	return value.String(), true
}
