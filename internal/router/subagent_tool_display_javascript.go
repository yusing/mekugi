package router

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"

	"github.com/yusing/mekugi/internal/sourceparse"

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
	if parser.SetLanguage(sourceparse.JavaScript) != nil {
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
	bindings := make(map[string]string)
	for i := 0; i < len(statements); i++ {
		first := statements[i]
		var expression *sitter.Node
		batchProjection := false
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
			value := declaration.ChildByFieldName("value")
			if first.Child(0).Kind() == "const" && !requireResultMetadata {
				if literal, ok := sourceparse.JavaScriptValue(value, bytes); ok {
					if value, ok := literal.(string); ok {
						bindings[name] = value
						continue
					}
				}
			}
			if !toolActivityResultProjection(statements[i+1], bytes, name, requireResultMetadata) {
				batchProjection = !requireResultMetadata &&
					(toolActivityBatchForEachProjection(statements[i+1], bytes, name) ||
						toolActivityBatchIndexedProjection(statements[i+1], bytes, name))
				if !batchProjection {
					return nil, false
				}
			}
			expression = value
			i++
		}
		if expression != nil && expression.Kind() == "await_expression" && !requireResultMetadata {
			if args, ok := toolActivityCallArguments(expression.NamedChild(0), bytes, "tools", applyPatchToolName); ok && len(args) == 1 && args[0].Kind() == "identifier" {
				if patch, found := bindings[args[0].Utf8Text(bytes)]; found {
					calls = append(calls, map[string]json.RawMessage{"name": mustMarshalJSON(applyPatchToolName), "input": mustMarshalJSON(patch)})
					continue
				}
			}
		}
		if !requireResultMetadata && expression != nil && expression.Kind() == "member_expression" &&
			expression.ChildByFieldName("optional_chain") == nil {
			property := expression.ChildByFieldName("property")
			object := expression.ChildByFieldName("object")
			if property != nil && property.Kind() == "property_identifier" && property.Utf8Text(bytes) == "output" &&
				object != nil && object.Kind() == "parenthesized_expression" && object.NamedChildCount() == 1 {
				expression = object.NamedChild(0)
			}
		}
		nested, ok := toolActivityAwaitedCalls(expression, bytes, requireResultMetadata)
		if !ok || batchProjection && !toolActivityPromiseBatch(expression, bytes) {
			return nil, false
		}
		calls = append(calls, nested...)
	}
	return calls, true
}

func toolActivityProtectedBindingName(node *sitter.Node, source []byte) bool {
	if node == nil {
		return false
	}
	if node.Kind() == "identifier" || node.Kind() == "shorthand_property_identifier_pattern" {
		if strings.ContainsRune(node.Utf8Text(source), '\\') {
			return true // An escape may spell a protected binding after JS decoding.
		}
		return slices.Contains([]string{"tools", "Promise"}, node.Utf8Text(source))
	}
	for i := range node.NamedChildCount() {
		if toolActivityProtectedBindingName(node.NamedChild(uint(i)), source) {
			return true
		}
	}
	return false
}

func toolActivityPromiseBatch(expression *sitter.Node, source []byte) bool {
	if expression == nil || expression.Kind() != "await_expression" {
		return false
	}
	call := expression.NamedChild(0)
	for _, method := range []string{"all", "allSettled"} {
		if args, ok := toolActivityCallArguments(call, source, "Promise", method); ok &&
			len(args) == 1 && args[0].Kind() == "array" {
			return true
		}
	}
	return false
}

// Accept the common compact projection used for a Promise batch:
//
//	results.forEach((result, i) => text(JSON.stringify({i, result})))
//
// The callback must only print a JSON object made from its own parameters.
// This keeps display recognition presentation-only and rejects callbacks with
// additional effects or values from outside the callback.
func toolActivityBatchForEachProjection(statement *sitter.Node, source []byte, binding string) bool {
	if statement.Kind() != "expression_statement" || statement.NamedChildCount() != 1 {
		return false
	}
	call := statement.NamedChild(0)
	args, ok := toolActivityCallArguments(call, source, binding, "forEach")
	if !ok || len(args) != 1 || args[0].Kind() != "arrow_function" {
		return false
	}
	callback := args[0]
	parameters := callback.ChildByFieldName("parameters")
	body := callback.ChildByFieldName("body")
	if parameters == nil || body == nil || parameters.Kind() != "formal_parameters" {
		return false
	}
	allowed := make(map[string]bool, parameters.NamedChildCount())
	for i := range parameters.NamedChildCount() {
		parameter := parameters.NamedChild(uint(i))
		if parameter.Kind() != "identifier" {
			return false
		}
		name := parameter.Utf8Text(source)
		if strings.ContainsRune(name, '\\') || name == "text" || name == "JSON" {
			return false
		}
		allowed[name] = true
	}
	if len(allowed) == 0 {
		return false
	}
	return toolActivityBatchPrintedObject(body, source, allowed)
}

// The indexed form consumes the same completed batch, once per result. Its
// loop header is checked structurally so unrelated work cannot be hidden.
func toolActivityBatchIndexedProjection(statement *sitter.Node, source []byte, binding string) bool {
	return toolActivityBatchIndexedPresentation(statement, source, binding, map[string]bool{binding: true})
}

func toolActivityBatchIndexedPresentation(statement *sitter.Node, source []byte, binding string, allowed map[string]bool) bool {
	if statement.Kind() != "for_statement" {
		return false
	}
	initializer := statement.ChildByFieldName("initializer")
	condition := statement.ChildByFieldName("condition")
	increment := statement.ChildByFieldName("increment")
	body := statement.ChildByFieldName("body")
	if initializer == nil || initializer.Kind() != "lexical_declaration" || initializer.NamedChildCount() != 1 ||
		initializer.ChildByFieldName("kind").Kind() != "let" || condition == nil ||
		condition.Kind() != "binary_expression" || increment == nil || increment.Kind() != "update_expression" ||
		body == nil {
		return false
	}
	declaration := initializer.NamedChild(0)
	index := declaration.ChildByFieldName("name")
	start := declaration.ChildByFieldName("value")
	if index == nil || index.Kind() != "identifier" || start == nil || start.Kind() != "number" ||
		start.Utf8Text(source) != "0" {
		return false
	}
	indexName := index.Utf8Text(source)
	if indexName == binding || slices.Contains([]string{"text", "JSON", "tools", "Promise"}, indexName) ||
		strings.ContainsRune(indexName, '\\') ||
		!toolActivityMemberPath(condition.ChildByFieldName("left"), source, indexName) ||
		!toolActivityMemberPath(condition.ChildByFieldName("right"), source, binding, "length") ||
		condition.ChildByFieldName("operator").Kind() != "<" ||
		!toolActivityMemberPath(increment.ChildByFieldName("argument"), source, indexName) ||
		increment.ChildByFieldName("operator").Kind() != "++" {
		return false
	}
	locals := maps.Clone(allowed)
	locals[indexName] = true
	return toolActivityPresentationStatement(body, source, locals)
}

// A forEach callback prints an object built only from its local result and
// index references. This inspects output shape, not JS execution.
func toolActivityBatchPrintedObject(body *sitter.Node, source []byte, allowed map[string]bool) bool {
	textArgs, ok := toolActivityCallArguments(body, source, "text")
	if !ok || len(textArgs) != 1 {
		return false
	}
	jsonArgs, ok := toolActivityCallArguments(textArgs[0], source, "JSON", "stringify")
	if !ok || len(jsonArgs) != 1 || jsonArgs[0].Kind() != "object" {
		return false
	}
	object := jsonArgs[0]
	if object.NamedChildCount() == 0 {
		return false
	}
	for i := range object.NamedChildCount() {
		member := object.NamedChild(uint(i))
		var value *sitter.Node
		switch member.Kind() {
		case "pair":
			key := member.ChildByFieldName("key")
			value = member.ChildByFieldName("value")
			if key == nil || key.Kind() != "property_identifier" && key.Kind() != "string" ||
				value == nil {
				return false
			}
		case "shorthand_property_identifier":
			value = member
		case "spread_element":
			if member.NamedChildCount() != 1 {
				return false
			}
			value = member.NamedChild(0)
		default:
			return false
		}
		if value == nil || (value.Kind() != "identifier" && value.Kind() != "shorthand_property_identifier") || !allowed[value.Utf8Text(source)] {
			return false
		}
	}
	return true
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
	name := ""
	if property != nil {
		name = property.Utf8Text(bytes)
	} else {
		return nil, false
	}
	switch name {
	case "exec_command", "view_image", "write_stdin", "apply_patch":
	default:
		_, _, mcp := toolActivityMCPName(name)
		plugin := false
		if !strings.HasPrefix(name, "mcp__") {
			group, tool, qualified := strings.Cut(name, "__")
			plugin = qualified && group != "" && tool != ""
		}
		if !mcp && !plugin && toolActivityBuiltinLabel(name) == "" {
			return nil, false
		}
	}
	if !toolActivityMemberPath(callee, bytes, "tools", name) || call.ChildByFieldName("optional_chain") != nil {
		return nil, false
	}
	var value any
	if args.NamedChildCount() == 1 {
		var ok bool
		value, ok = sourceparse.JavaScriptValue(args.NamedChild(0), bytes)
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
	target, targetOK := sourceparse.JavaScriptValue(args[0], source)
	metadata, metadataOK := sourceparse.JavaScriptValue(args[2], source)
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

func toolActivityPresentationStatement(node *sitter.Node, source []byte, allowed map[string]bool) bool {
	if node == nil {
		return false
	}
	switch node.Kind() {
	case "comment", "empty_statement":
		return true
	case "statement_block":
		locals := maps.Clone(allowed)
		for i := range node.NamedChildCount() {
			if !toolActivityPresentationStatement(node.NamedChild(uint(i)), source, locals) {
				return false
			}
		}
		return true
	case "lexical_declaration":
		if node.Child(0).Kind() != "const" || node.NamedChildCount() != 1 {
			return false
		}
		declaration := node.NamedChild(0)
		name := declaration.ChildByFieldName("name")
		if name == nil || name.Kind() != "identifier" || toolActivityProtectedBindingName(name, source) {
			return false
		}
		key := name.Utf8Text(source)
		if allowed[key] || key == "text" || key == "JSON" || !toolActivityPresentationValue(declaration.ChildByFieldName("value"), source, allowed) {
			return false
		}
		allowed[key] = true
		return true
	case "expression_statement":
		args, ok := toolActivityCallArguments(node.NamedChild(0), source, "text")
		return ok && len(args) == 1 && toolActivityPresentationValue(args[0], source, allowed)
	case "if_statement":
		if !toolActivityPresentationValue(node.ChildByFieldName("condition"), source, allowed) ||
			!toolActivityPresentationStatement(node.ChildByFieldName("consequence"), source, maps.Clone(allowed)) {
			return false
		}
		alternative := node.ChildByFieldName("alternative")
		return alternative == nil || toolActivityPresentationStatement(alternative, source, maps.Clone(allowed))
	case "else_clause":
		return node.NamedChildCount() == 1 && toolActivityPresentationStatement(node.NamedChild(0), source, allowed)
	case "continue_statement":
		return node.NamedChildCount() == 0
	case "for_in_statement":
		kind, operator := node.ChildByFieldName("kind"), node.ChildByFieldName("operator")
		left, right := node.ChildByFieldName("left"), node.ChildByFieldName("right")
		if kind == nil || kind.Kind() != "const" || operator == nil || operator.Kind() != "of" ||
			left == nil || left.Kind() != "identifier" || toolActivityProtectedBindingName(left, source) ||
			right == nil || right.Kind() != "identifier" || !allowed[right.Utf8Text(source)] {
			return false
		}
		name := left.Utf8Text(source)
		if allowed[name] || name == "text" || name == "JSON" {
			return false
		}
		locals := maps.Clone(allowed)
		locals[name] = true
		return toolActivityPresentationStatement(node.ChildByFieldName("body"), source, locals)
	}
	return false
}

func toolActivityPresentationValue(node *sitter.Node, source []byte, allowed map[string]bool) bool {
	if node == nil {
		return false
	}
	if _, ok := sourceparse.JavaScriptValue(node, source); ok {
		return true
	}
	switch node.Kind() {
	case "identifier", "shorthand_property_identifier":
		return allowed[node.Utf8Text(source)]
	case "member_expression":
		property := node.ChildByFieldName("property")
		return node.ChildByFieldName("optional_chain") == nil && property != nil && property.Kind() == "property_identifier" &&
			toolActivityPresentationValue(node.ChildByFieldName("object"), source, allowed)
	case "subscript_expression":
		index := node.ChildByFieldName("index")
		return node.ChildByFieldName("optional_chain") == nil && index != nil && (index.Kind() == "identifier" || index.Kind() == "number") &&
			toolActivityPresentationValue(index, source, allowed) && toolActivityPresentationValue(node.ChildByFieldName("object"), source, allowed)
	case "call_expression":
		args, ok := toolActivityCallArguments(node, source, "JSON", "stringify")
		return ok && len(args) == 1 && toolActivityPresentationValue(args[0], source, allowed)
	case "binary_expression":
		operator := node.ChildByFieldName("operator")
		if operator == nil || !slices.Contains([]string{"+", "===", "!==", "==", "!=", "&&", "||", "??"}, operator.Kind()) {
			return false
		}
	case "object":
		for i := range node.NamedChildCount() {
			field := node.NamedChild(uint(i))
			if field.Kind() == "pair" {
				key := field.ChildByFieldName("key")
				if key == nil || key.Kind() != "property_identifier" && key.Kind() != "string" || !toolActivityPresentationValue(field.ChildByFieldName("value"), source, allowed) {
					return false
				}
			} else if field.Kind() == "shorthand_property_identifier" || field.Kind() == "spread_element" {
				if !toolActivityPresentationValue(field, source, allowed) {
					return false
				}
			} else {
				return false
			}
		}
		return true
	case "parenthesized_expression", "ternary_expression", "template_string", "template_substitution", "spread_element":
	case "string_fragment", "escape_sequence":
		return true
	default:
		return false
	}
	for i := range node.NamedChildCount() {
		if !toolActivityPresentationValue(node.NamedChild(uint(i)), source, allowed) {
			return false
		}
	}
	return true
}
