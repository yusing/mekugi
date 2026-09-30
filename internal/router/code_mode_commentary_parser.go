package router

import (
	"errors"

	sitter "github.com/tree-sitter/go-tree-sitter"
	treeSitterJavaScript "github.com/tree-sitter/tree-sitter-javascript/bindings/go"
)

var codeModeJavaScriptLanguage = sitter.NewLanguage(treeSitterJavaScript.Language())

func findCodeModeCommentaryCalls(source string) ([]codeModeCommentaryCall, error) {
	sourceBytes := []byte(source)
	tree, err := parseSourceTree(sourceBytes, codeModeJavaScriptLanguage, nil)
	if err != nil {
		return nil, err
	}
	if tree == nil {
		return nil, errors.New("parse Code Mode commentary program")
	}
	defer tree.Close()
	root := tree.RootNode()
	if root == nil || root.HasError() {
		// Commentary is auxiliary, not JavaScript validation. Leave programs the
		// parser cannot understand intact for the executor, including its normal
		// syntax-error reporting. Never rewrite a partially recovered parse tree.
		return nil, nil
	}
	var calls []codeModeCommentaryCall
	var walk func(*sitter.Node)
	walk = func(node *sitter.Node) {
		if node.Kind() == "await_expression" && node.NamedChildCount() == 1 {
			call := node.NamedChild(0)
			if call != nil && call.Kind() == "call_expression" {
				callee := call.ChildByFieldName("function")
				arguments := call.ChildByFieldName("arguments")
				if callee != nil && callee.Kind() == "identifier" && callee.Utf8Text(sourceBytes) == commentaryArgumentName &&
					arguments != nil && arguments.NamedChildCount() == 1 {
					argument := arguments.NamedChild(0)
					calls = append(calls, codeModeCommentaryCall{
						start:         int(node.StartByte()),
						end:           int(node.EndByte()),
						argumentStart: int(argument.StartByte()),
						argumentEnd:   int(argument.EndByte()),
					})
				}
			}
		}
		for index := range node.NamedChildCount() {
			if child := node.NamedChild(uint(index)); child != nil {
				walk(child)
			}
		}
	}
	walk(root)
	return calls, nil
}
