package router

import (
	"strconv"

	sitter "github.com/tree-sitter/go-tree-sitter"
)

// Rewrite actual tool calls, never quoted code or comments. The runtime wrapper
// evaluates dynamic arguments once and leaves input/cancellation calls untouched.
func rewriteCodeModeWaits(source string, policies map[string]waitPolicy) (string, bool) {
	if len(policies) == 0 {
		return source, false
	}
	parser := sitter.NewParser()
	defer parser.Close()
	if parser.SetLanguage(codeModeJavaScriptLanguage) != nil {
		return source, false
	}
	body := []byte(source)
	tree := parser.Parse(body, nil)
	if tree == nil {
		return source, false
	}
	defer tree.Close()
	root := tree.RootNode()
	if root == nil || root.HasError() {
		return source, false
	}
	// A local tools binding does not establish host-tool identity.
	var shadowed bool
	var containsTools func(*sitter.Node) bool
	containsTools = func(node *sitter.Node) bool {
		if node == nil {
			return false
		}
		if node.Utf8Text(body) == "tools" {
			return true
		}
		for i := range node.NamedChildCount() {
			if containsTools(node.NamedChild(uint(i))) {
				return true
			}
		}
		return false
	}
	var checkBindings func(*sitter.Node)
	checkBindings = func(node *sitter.Node) {
		switch node.Kind() {
		case "variable_declarator", "function_declaration", "function_expression",
			"generator_function_declaration", "generator_function", "class_declaration", "class":
			shadowed = shadowed || containsTools(node.ChildByFieldName("name"))
		case "formal_parameters", "import_statement":
			shadowed = shadowed || containsTools(node)
		case "arrow_function", "catch_clause":
			shadowed = shadowed || containsTools(node.ChildByFieldName("parameter"))
		case "assignment_expression", "augmented_assignment_expression", "for_in_statement":
			shadowed = shadowed || containsTools(node.ChildByFieldName("left"))
		}
		for i := range node.NamedChildCount() {
			checkBindings(node.NamedChild(uint(i)))
		}
	}
	checkBindings(root)
	if shadowed {
		return source, false
	}
	type replacement struct {
		start, end int
		text       string
	}
	var changes []replacement
	var walk func(*sitter.Node)
	walk = func(node *sitter.Node) {
		if node.Kind() == "call_expression" {
			callee := node.ChildByFieldName("function")
			if callee != nil && (callee.Kind() == "member_expression" || callee.Kind() == "subscript_expression") {
				object := callee.ChildByFieldName("object")
				property := callee.ChildByFieldName("property")
				if callee.Kind() == "subscript_expression" {
					property = callee.ChildByFieldName("index")
					if property != nil && property.Kind() != "string" {
						property = nil
					}
				}
				if object != nil && object.Kind() == "identifier" && object.Utf8Text(body) == "tools" && property != nil {
					name := property.Utf8Text(body)
					if property.Kind() == "string" && len(name) >= 2 {
						name = name[1 : len(name)-1]
					}
					if policy, ok := policies[name]; ok {
						check := "a && typeof a === 'object' && !Array.isArray(a) && (a.terminate === undefined || a.terminate === false)"
						if name == "write_stdin" {
							check += " && (a.chars === undefined || a.chars === '')"
						}
						field := strconv.Quote(policy.field)
						floor := strconv.Itoa(policy.floor)
						text := "new Proxy(" + callee.Utf8Text(body) + ", {apply(fn, _receiver, args) { const a = args[0]; if (" + check +
							" && (a[" + field + "] === undefined || (typeof a[" + field + "] === 'number' && Number.isFinite(a[" + field + "]) && a[" + field + "] >= 0 && a[" + field + "] < " + floor +
							"))) args[0] = {...a, [" + field + "]: " + floor + "}; return Reflect.apply(fn, tools, args); }})"
						changes = append(changes, replacement{int(callee.StartByte()), int(callee.EndByte()), "(" + text + ")"})
					}
				}
			}
		}
		for i := range node.NamedChildCount() {
			walk(node.NamedChild(uint(i)))
		}
	}
	walk(root)
	for i := len(changes) - 1; i >= 0; i-- {
		change := changes[i]
		source = source[:change.start] + change.text + source[change.end:]
	}
	return source, len(changes) != 0
}
