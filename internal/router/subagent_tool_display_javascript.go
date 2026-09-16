package router

import (
	"encoding/json"
	"math"
	"slices"
	"strconv"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
)

func toolActivityUnwrapExec(source string, requireResultMetadata bool) (map[string]json.RawMessage, bool) {
	calls, ok := toolActivityUnwrapExecCalls(source, requireResultMetadata)
	if !ok || len(calls) != 1 {
		return nil, false
	}
	return calls[0], true
}

func toolActivityUnwrapExecCalls(source string, requireResultMetadata bool) ([]map[string]json.RawMessage, bool) {
	parser := sitter.NewParser()
	defer parser.Close()
	if parser.SetLanguage(codeModeJavaScriptLanguage) != nil {
		return nil, false
	}
	bytes := []byte(source)
	tree := parser.Parse(bytes, nil)
	if tree == nil {
		return nil, false
	}
	defer tree.Close()
	root := tree.RootNode()
	if root.HasError() {
		return nil, false
	}
	var statements []*sitter.Node
	for i := range root.NamedChildCount() {
		node := root.NamedChild(uint(i))
		if node.Kind() != "comment" {
			statements = append(statements, node)
		}
	}
	if len(statements) == 0 {
		return nil, false
	}
	var calls []map[string]json.RawMessage
	for i := 0; i < len(statements); i++ {
		first := statements[i]
		var expression *sitter.Node
		if first.Kind() == "expression_statement" {
			expression = first.NamedChild(0)
			if args, ok := toolActivityCallArguments(expression, bytes, "text"); ok && len(args) == 1 {
				expression = args[0]
			} else if args, ok := toolActivityCallArguments(expression, bytes, "generatedImage"); ok && len(args) == 1 && !requireResultMetadata {
				expression = args[0]
			} else if requireResultMetadata {
				return nil, false
			}
		} else if first.Kind() == "lexical_declaration" && first.NamedChildCount() == 1 && i+1 < len(statements) {
			declaration := first.NamedChild(0)
			binding := declaration.ChildByFieldName("name")
			if binding == nil || binding.Kind() != "identifier" {
				return nil, false
			}
			name := binding.Utf8Text(bytes)
			// A local runtime binding changes every call in the program, including earlier calls.
			if slices.Contains([]string{"tools", "text", "JSON", "Object", "Promise", "generatedImage"}, name) {
				return nil, false
			}
			if !toolActivityResultProjection(statements[i+1], bytes, name, requireResultMetadata) {
				return nil, false
			}
			expression = declaration.ChildByFieldName("value")
			i++
		}
		nested, ok := toolActivityAwaitedCalls(expression, bytes, requireResultMetadata)
		if !ok {
			return nil, false
		}
		calls = append(calls, nested...)
	}
	return calls, true
}

func toolActivityAwaitedCalls(expression *sitter.Node, bytes []byte, requireResultMetadata bool) ([]map[string]json.RawMessage, bool) {
	if expression == nil || expression.Kind() != "await_expression" {
		return nil, false
	}
	call := expression.NamedChild(0)
	if call == nil || call.Kind() != "call_expression" {
		return nil, false
	}
	if !requireResultMetadata {
		for _, method := range []string{"all", "allSettled"} {
			if args, ok := toolActivityCallArguments(call, bytes, "Promise", method); ok && len(args) == 1 && args[0].Kind() == "array" {
				var calls []map[string]json.RawMessage
				for i := range args[0].NamedChildCount() {
					node := args[0].NamedChild(uint(i))
					item, ok := toolActivityStaticToolCall(node, bytes)
					if !ok {
						return nil, false
					}
					calls = append(calls, item)
				}
				return calls, len(calls) > 0
			}
		}
	}
	item, ok := toolActivityStaticToolCall(call, bytes)
	if !ok {
		return nil, false
	}
	return []map[string]json.RawMessage{item}, true
}

func toolActivityStaticToolCall(call *sitter.Node, bytes []byte) (map[string]json.RawMessage, bool) {
	if call == nil || call.Kind() != "call_expression" {
		return nil, false
	}
	callee, args := call.ChildByFieldName("function"), call.ChildByFieldName("arguments")
	if callee == nil || args == nil || args.NamedChildCount() > 1 {
		return nil, false
	}
	property := callee.ChildByFieldName("property")
	if property == nil {
		return nil, false
	}
	name := property.Utf8Text(bytes)
	switch name {
	case "exec_command", "shell_command", "shell", "view_image", "write_stdin", "apply_patch":
	default:
		if _, _, ok := toolActivityMCPName(name); !ok && toolActivityBuiltinLabel(name) == "" {
			return nil, false
		}
	}
	if !toolActivityMemberPath(callee, bytes, "tools", name) || call.ChildByFieldName("optional_chain") != nil {
		return nil, false
	}
	var value any
	if args.NamedChildCount() == 1 {
		var ok bool
		value, ok = toolActivityStaticJavaScriptValue(args.NamedChild(0), bytes)
		if !ok {
			return nil, false
		}
	}
	item := map[string]json.RawMessage{"name": mustMarshalJSON(name)}
	if text, ok := value.(string); ok {
		item["input"] = mustMarshalJSON(text)
	} else if args.NamedChildCount() != 0 {
		item["arguments"] = mustMarshalJSON(string(mustMarshalJSON(value)))
	}
	return item, true
}

// Inspect the projection tree so formatting never determines whether a wrapper
// is transparent. Only the bound result and the supported metadata copy qualify.
func toolActivityResultProjection(statement *sitter.Node, source []byte, binding string, requireResultMetadata bool) bool {
	if statement.Kind() != "expression_statement" || statement.NamedChildCount() != 1 {
		return false
	}
	args, ok := toolActivityCallArguments(statement.NamedChild(0), source, "text")
	if !ok && !requireResultMetadata {
		args, ok = toolActivityCallArguments(statement.NamedChild(0), source, "generatedImage")
		return ok && len(args) == 1 && toolActivityMemberPath(args[0], source, binding)
	}
	if !ok || len(args) != 1 {
		return false
	}
	value := args[0]
	if toolActivityMemberPath(value, source, binding, "output") {
		return !requireResultMetadata
	}
	if toolActivityMemberPath(value, source, binding) {
		return true
	}
	args, ok = toolActivityCallArguments(value, source, "JSON", "stringify")
	if !ok || len(args) != 1 {
		return false
	}
	if toolActivityMemberPath(args[0], source, binding) {
		return true
	}
	args, ok = toolActivityCallArguments(args[0], source, "Object", "assign")
	if !ok || len(args) != 3 || !toolActivityMemberPath(args[1], source, binding) {
		return false
	}
	target, targetOK := toolActivityStaticJavaScriptValue(args[0], source)
	metadata, metadataOK := toolActivityStaticJavaScriptValue(args[2], source)
	targetObject, targetIsObject := target.(map[string]any)
	metadataObject, metadataIsObject := metadata.(map[string]any)
	return targetOK && metadataOK && targetIsObject && metadataIsObject &&
		len(targetObject) == 0 && len(metadataObject) == 1 && metadataObject["retained"] == false
}

func toolActivityCallArguments(node *sitter.Node, source []byte, path ...string) ([]*sitter.Node, bool) {
	if node == nil || node.Kind() != "call_expression" ||
		node.ChildByFieldName("optional_chain") != nil ||
		!toolActivityMemberPath(node.ChildByFieldName("function"), source, path...) {
		return nil, false
	}
	args := node.ChildByFieldName("arguments")
	if args == nil || args.Kind() != "arguments" {
		return nil, false
	}
	var values []*sitter.Node
	for i := range args.NamedChildCount() {
		child := args.NamedChild(uint(i))
		if child.Kind() != "comment" {
			values = append(values, child)
		}
	}
	return values, true
}

func toolActivityMemberPath(node *sitter.Node, source []byte, path ...string) bool {
	if node == nil || len(path) == 0 {
		return false
	}
	if len(path) == 1 {
		return node.Kind() == "identifier" && node.Utf8Text(source) == path[0]
	}
	if node.Kind() != "member_expression" || node.ChildByFieldName("optional_chain") != nil {
		return false
	}
	property := node.ChildByFieldName("property")
	return property != nil && property.Kind() == "property_identifier" &&
		property.Utf8Text(source) == path[len(path)-1] &&
		toolActivityMemberPath(node.ChildByFieldName("object"), source, path[:len(path)-1]...)
}

func toolActivityStaticJavaScriptValue(node *sitter.Node, source []byte) (any, bool) {
	if node == nil {
		return nil, false
	}
	switch node.Kind() {
	case "parenthesized_expression":
		if node.NamedChildCount() != 1 {
			return nil, false
		}
		return toolActivityStaticJavaScriptValue(node.NamedChild(0), source)
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
				key, ok = toolActivityJavaScriptString(keyNode.Utf8Text(source))
				if !ok {
					return nil, false
				}
			default:
				return nil, false
			}
			decoded, ok := toolActivityStaticJavaScriptValue(valueNode, source)
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
			decoded, ok := toolActivityStaticJavaScriptValue(child, source)
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
	case "string":
		return toolActivityJavaScriptString(node.Utf8Text(source))
	case "number":
		return toolActivityJSONNumber(node.Utf8Text(source))
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
		return toolActivityJSONNumber(node.Utf8Text(source))
	default:
		return nil, false
	}
}
func toolActivityJSONNumber(source string) (any, bool) {
	if !json.Valid([]byte(source)) {
		return nil, false
	}
	value, err := strconv.ParseFloat(source, 64)
	if err != nil || math.IsInf(value, 0) {
		return nil, false
	}
	return value, true
}

func toolActivityJavaScriptString(source string) (string, bool) {
	if len(source) < 2 || source[0] != source[len(source)-1] || source[0] != '"' && source[0] != '\'' {
		return "", false
	}
	quote := source[0]
	rest := source[1 : len(source)-1]
	var value strings.Builder
	for rest != "" {
		if rest[0] == '\\' {
			if len(rest) < 2 {
				return "", false
			}
			switch rest[1] {
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
