package sourceparse

import sitter "github.com/tree-sitter/go-tree-sitter"

func Call(node *sitter.Node) (*sitter.Node, []*sitter.Node) {
	if node == nil || node.Kind() != "call" && node.Kind() != "call_expression" {
		return nil, nil
	}
	function := node.ChildByFieldName("function")
	args := node.ChildByFieldName("arguments")
	var values []*sitter.Node
	if args != nil {
		for i := range args.NamedChildCount() {
			values = append(values, args.NamedChild(uint(i)))
		}
	}
	return function, values
}
