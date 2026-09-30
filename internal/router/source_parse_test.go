package router

import "testing"

func TestParseSourceTreeCancellationModesPreserveSyntax(t *testing.T) {
	for _, test := range []struct {
		name     string
		source   string
		hasError bool
	}{
		{"valid", "const value = 1;\nvalue();\n", false},
		{"recovered", "const value = ;\nvalue();\n", true},
		{"empty", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			ordinary, err := parseSourceTree([]byte(test.source), codeModeJavaScriptLanguage, nil)
			if err != nil || ordinary == nil {
				t.Fatalf("ordinary parse = %v, %v", ordinary, err)
			}
			defer ordinary.Close()
			interruptible, err := parseSourceTree([]byte(test.source), codeModeJavaScriptLanguage, func() bool { return false })
			if err != nil || interruptible == nil {
				t.Fatalf("interruptible parse = %v, %v", interruptible, err)
			}
			defer interruptible.Close()
			left, right := ordinary.RootNode(), interruptible.RootNode()
			if left == nil || right == nil {
				t.Fatal("parse lost the root node")
			}
			if left.Kind() != "program" || left.HasError() != test.hasError || int(left.EndByte()) != len(test.source) {
				t.Fatalf("ordinary root: kind %q, hasError %v, endByte %d", left.Kind(), left.HasError(), left.EndByte())
			}
			if right.Kind() != left.Kind() || right.HasError() != left.HasError() || right.EndByte() != left.EndByte() || right.NamedChildCount() != left.NamedChildCount() {
				t.Fatalf("interruptible root changed: kind %q, hasError %v, endByte %d, namedChildren %d; ordinary namedChildren %d", right.Kind(), right.HasError(), right.EndByte(), right.NamedChildCount(), left.NamedChildCount())
			}
			if test.source != "" && left.NamedChildCount() == 0 {
				t.Fatal("parse lost all statements")
			}
		})
	}
}
