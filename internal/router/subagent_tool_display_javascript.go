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
			if slices.Contains([]string{"tools", "text", "JSON", "Object", "Promise", "generatedImage", "journal"}, name) {
				return nil, false
			}
			value := declaration.ChildByFieldName("value")
			if first.Child(0).Kind() == "const" && !requireResultMetadata {
				if literal, ok := toolActivityStaticJavaScriptValue(value, bytes); ok {
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

// toolActivityUnwrapExecOutput proves that a program prints exactly the stdout of
// one awaited literal tool call, as `text((await tools.exec_command({...})).output)`
// or `const r = await tools.exec_command({...}); text(r.output)`.
func toolActivityUnwrapExecOutput(source string) (map[string]json.RawMessage, bool) {
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
		if node := root.NamedChild(uint(i)); node.Kind() != "comment" {
			statements = append(statements, node)
		}
	}
	printed := func(statement *sitter.Node) *sitter.Node {
		if statement.Kind() != "expression_statement" || statement.NamedChildCount() != 1 {
			return nil
		}
		args, ok := toolActivityCallArguments(statement.NamedChild(0), bytes, "text")
		if !ok || len(args) != 1 {
			return nil
		}
		return args[0]
	}
	var call *sitter.Node
	switch len(statements) {
	case 1:
		value := printed(statements[0])
		if value == nil || value.Kind() != "member_expression" || value.ChildByFieldName("optional_chain") != nil {
			return nil, false
		}
		property, object := value.ChildByFieldName("property"), value.ChildByFieldName("object")
		if property == nil || property.Kind() != "property_identifier" || property.Utf8Text(bytes) != "output" ||
			object == nil || object.Kind() != "parenthesized_expression" || object.NamedChildCount() != 1 {
			return nil, false
		}
		call = object.NamedChild(0)
	case 2:
		declaration := statements[0]
		if declaration.Kind() != "lexical_declaration" || declaration.NamedChildCount() != 1 {
			return nil, false
		}
		name, value := declaration.NamedChild(0).ChildByFieldName("name"), declaration.NamedChild(0).ChildByFieldName("value")
		if name == nil || name.Kind() != "identifier" ||
			slices.Contains([]string{"tools", "text", "JSON", "Object", "Promise", "generatedImage", "journal"}, name.Utf8Text(bytes)) ||
			!toolActivityMemberPath(printed(statements[1]), bytes, name.Utf8Text(bytes), "output") {
			return nil, false
		}
		call = value
	default:
		return nil, false
	}
	calls, ok := toolActivityAwaitedCalls(call, bytes, true)
	if !ok || len(calls) != 1 {
		return nil, false
	}
	return calls[0], true
}

// A top-level literal Promise batch schedules its calls before any result
// presentation runs. When the presentation syntax is unknown, retain those
// proven calls for activity and label the remainder instead of discarding the
// whole batch. This is preview-only, never result or session evidence.
func toolActivityBatchProducerCalls(source string) ([]map[string]json.RawMessage, bool, bool) {
	if len(source) > maxMekugiScriptBytes {
		return nil, false, false
	}
	parser := sitter.NewParser()
	defer parser.Close()
	if parser.SetLanguage(codeModeJavaScriptLanguage) != nil {
		return nil, false, false
	}
	bytes := []byte(source)
	tree := parser.Parse(bytes, nil)
	if tree == nil {
		return nil, false, false
	}
	defer tree.Close()
	root := tree.RootNode()
	if root.HasError() || root.NamedChildCount() == 0 {
		return nil, false, false
	}
	var statements []*sitter.Node
	for i := range root.NamedChildCount() {
		statement := root.NamedChild(uint(i))
		if statement.Kind() != "comment" {
			statements = append(statements, statement)
		}
	}
	if len(statements) == 0 {
		return nil, false, false
	}
	first := statements[0]
	if first.Kind() != "lexical_declaration" || first.NamedChildCount() != 1 {
		return nil, false, false
	}
	declaration := first.NamedChild(0)
	name := declaration.ChildByFieldName("name")
	value := declaration.ChildByFieldName("value")
	if name == nil || name.Kind() != "identifier" ||
		strings.ContainsRune(name.Utf8Text(bytes), '\\') ||
		slices.Contains([]string{"tools", "Promise", "journal"}, name.Utf8Text(bytes)) ||
		!toolActivityPromiseBatch(value, bytes) {
		return nil, false, false
	}
	for _, statement := range statements[1:] {
		if toolActivityHoistedBinding(statement, bytes) {
			return nil, false, false
		}
		switch statement.Kind() {
		case "lexical_declaration":
			for j := range statement.NamedChildCount() {
				binding := statement.NamedChild(uint(j)).ChildByFieldName("name")
				if binding == nil || binding.Kind() != "identifier" ||
					strings.ContainsRune(binding.Utf8Text(bytes), '\\') ||
					slices.Contains([]string{"tools", "Promise", "journal"}, binding.Utf8Text(bytes)) {
					return nil, false, false
				}
			}
		case "class_declaration":
			if toolActivityProtectedBindingName(statement.ChildByFieldName("name"), bytes) {
				return nil, false, false
			}
		case "import_statement", "export_statement":
			// Module bindings may be instantiated before any top-level code.
			return nil, false, false
		}
	}
	calls, ok := toolActivityAwaitedCalls(value, bytes, false)
	return calls, len(statements) > 1, ok
}

// A protected var or function binding in a later statement can hoist into
// the producer's scope, even though the declaration appears after the batch.
func toolActivityHoistedBinding(node *sitter.Node, source []byte) bool {
	if node.Kind() == "function_declaration" &&
		toolActivityProtectedBindingName(node.ChildByFieldName("name"), source) {
		return true
	}
	if node.Kind() == "variable_declaration" {
		for i := range node.NamedChildCount() {
			if toolActivityProtectedBindingName(node.NamedChild(uint(i)).ChildByFieldName("name"), source) {
				return true
			}
		}
	}
	if node.Kind() == "for_in_statement" {
		for i := range node.ChildCount() {
			if node.Child(uint(i)).Kind() == "var" &&
				toolActivityProtectedBindingName(node.ChildByFieldName("left"), source) {
				return true
			}
		}
	}
	for i := range node.NamedChildCount() {
		if toolActivityHoistedBinding(node.NamedChild(uint(i)), source) {
			return true
		}
	}
	return false
}

func toolActivityProtectedBindingName(node *sitter.Node, source []byte) bool {
	if node == nil {
		return false
	}
	if node.Kind() == "identifier" || node.Kind() == "shorthand_property_identifier_pattern" {
		if strings.ContainsRune(node.Utf8Text(source), '\\') {
			return true // An escape may spell a protected binding after JS decoding.
		}
		return slices.Contains([]string{"tools", "Promise", "journal"}, node.Utf8Text(source))
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
	return toolActivityBatchPrintedObject(body, source, allowed, "", "")
}

// The indexed form consumes the same completed batch, once per result. Its
// loop header is checked structurally so unrelated work cannot be hidden.
func toolActivityBatchIndexedProjection(statement *sitter.Node, source []byte, binding string) bool {
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
		body == nil || body.Kind() != "expression_statement" || body.NamedChildCount() != 1 {
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
	if indexName == binding || slices.Contains([]string{"text", "JSON", "tools", "Promise", "journal"}, indexName) ||
		strings.ContainsRune(indexName, '\\') ||
		!toolActivityMemberPath(condition.ChildByFieldName("left"), source, indexName) ||
		!toolActivityMemberPath(condition.ChildByFieldName("right"), source, binding, "length") ||
		condition.ChildByFieldName("operator").Kind() != "<" ||
		!toolActivityMemberPath(increment.ChildByFieldName("argument"), source, indexName) ||
		increment.ChildByFieldName("operator").Kind() != "++" {
		return false
	}
	return toolActivityBatchPrintedObject(body.NamedChild(0), source, map[string]bool{indexName: true}, binding, indexName)
}

// Both callback and indexed loops print an object built only from their local
// result and index references. This inspects output shape, not JS execution.
func toolActivityBatchPrintedObject(body *sitter.Node, source []byte, allowed map[string]bool, binding, index string) bool {
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
		if !toolActivityBatchReference(value, source, allowed, binding, index) {
			return false
		}
	}
	return true
}

func toolActivityBatchReference(node *sitter.Node, source []byte, allowed map[string]bool, binding, index string) bool {
	if node == nil {
		return false
	}
	if node.Kind() == "identifier" || node.Kind() == "shorthand_property_identifier" {
		return allowed[node.Utf8Text(source)]
	}
	return binding != "" && node.Kind() == "subscript_expression" &&
		node.ChildByFieldName("optional_chain") == nil &&
		toolActivityMemberPath(node.ChildByFieldName("object"), source, binding) &&
		toolActivityMemberPath(node.ChildByFieldName("index"), source, index)
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
	if !ok || requireResultMetadata && jsonString(item, "name") == journalToolName {
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
	if toolActivityMemberPath(callee, bytes, journalToolName) {
		name = journalToolName
	} else if property != nil {
		name = property.Utf8Text(bytes)
	} else {
		return nil, false
	}
	switch name {
	case "exec_command", "view_image", "write_stdin", "apply_patch", journalToolName:
	default:
		if _, _, ok := toolActivityMCPName(name); !ok && toolActivityBuiltinLabel(name) == "" {
			return nil, false
		}
	}
	bareJournal := name == journalToolName && toolActivityMemberPath(callee, bytes, journalToolName)
	if !bareJournal && !toolActivityMemberPath(callee, bytes, "tools", name) || call.ChildByFieldName("optional_chain") != nil {
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
	case "string", "template_string":
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
