package sourcekind

import "testing"

func TestClassify(t *testing.T) {
	tests := []struct {
		path       string
		language   string
		jsx        bool
		validation bool
		resolver   string
		ok         bool
	}{
		{path: "a.go", language: "go", resolver: "gopls", ok: true},
		{path: "a.rs", language: "rust", ok: true},
		{path: "a.d.ts", language: "typescript", resolver: "typescript", validation: true, ok: true},
		{path: "a.d.mts", language: "typescript", resolver: "typescript", ok: true},
		{path: "a.tsx", language: "typescript", resolver: "typescript", jsx: true, ok: true},
		{path: "a.jsx", language: "javascript", resolver: "typescript", jsx: true, ok: true},
		{path: "a.pyi", language: "python", resolver: "python", ok: true},
		{path: "a.GO"},
		{path: "a.RS"},
		{path: "a.txt"},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			format, ok := Classify(test.path)
			if ok != test.ok || format.Language != test.language || format.JSX != test.jsx || format.SyntaxValidation != test.validation || format.SemanticResolver != test.resolver {
				t.Fatalf("Classify(%q) = (%+v, %t)", test.path, format, ok)
			}
		})
	}
}
