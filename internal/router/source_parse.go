package router

import (
	sitter "github.com/tree-sitter/go-tree-sitter"
	treeSitterJavaScript "github.com/tree-sitter/tree-sitter-javascript/bindings/go"
)

var codeModeJavaScriptLanguage = sitter.NewLanguage(treeSitterJavaScript.Language())

// parseSourceTree leaves the tree and cancellation policy with its caller.
// A nil cancellation predicate uses the ordinary non-interruptible parser.
func parseSourceTree(data []byte, language *sitter.Language, canceled func() bool) (*sitter.Tree, error) {
	parser := sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(language); err != nil {
		return nil, err
	}
	if canceled == nil {
		return parser.Parse(data, nil), nil
	}
	return parser.ParseWithOptions(func(offset int, _ sitter.Point) []byte {
		if offset >= len(data) {
			return nil
		}
		return data[offset:]
	}, nil, &sitter.ParseOptions{ProgressCallback: func(sitter.ParseState) bool { return canceled() }}), nil
}
