// Package sourceparse shares read-only source input and syntax between
// classifiers and literal previews.
package sourceparse

import (
	sitter "github.com/tree-sitter/go-tree-sitter"
	treeSitterJavaScript "github.com/tree-sitter/tree-sitter-javascript/bindings/go"
	python "github.com/tree-sitter/tree-sitter-python/bindings/go"
	typescript "github.com/tree-sitter/tree-sitter-typescript/bindings/go"
)

var Python = sitter.NewLanguage(python.Language())
var TypeScript = sitter.NewLanguage(typescript.LanguageTypescript())

var JavaScript = sitter.NewLanguage(treeSitterJavaScript.Language())

// Parse leaves the tree and cancellation policy with its caller.
// A nil cancellation predicate uses the ordinary non-interruptible parser.
func Parse(data []byte, language *sitter.Language, canceled func() bool) (*sitter.Tree, error) {
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
