package sourceparse

import sitter "github.com/tree-sitter/go-tree-sitter"

// Source: internal/router/subagent_tool_display_javascript.go:397:431@23344db6.
func JavaScriptCallArguments(node *sitter.Node, source []byte, path ...string) ([]*sitter.Node, bool) {
	if node == nil || node.Kind() != "call_expression" ||
		node.ChildByFieldName("optional_chain") != nil ||
		!JavaScriptMemberPath(node.ChildByFieldName("function"), source, path...) {
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

func JavaScriptMemberPath(node *sitter.Node, source []byte, path ...string) bool {
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
		JavaScriptMemberPath(node.ChildByFieldName("object"), source, path[:len(path)-1]...)
}
