package structure

import (
	"reflect"
	"strings"
	"testing"

	"github.com/yusing/mekugi/libraries/sourceanalysis/sourcekind"
)

func TestOutlinePreservesStructureWithoutTokens(t *testing.T) {
	for _, tc := range []struct{ path, source string }{
		{"a.go", "package p\nfunc F() { println(1) }\n"},
		{"a.ts", "export function f() { return value; }\n"},
		{"bad.ts", "export function f( { return value;\n"},
		{"a.py", "def f():\n  return value\n"},
		{"a.json", `{"a":{"b":1}}`},
		{"bad.json", `{"a":{"b":`},
	} {
		t.Run(tc.path, func(t *testing.T) {
			format, _ := sourcekind.Classify(tc.path)
			full, err := Parse(t.Context(), tc.source, format)
			if err != nil {
				t.Fatal(err)
			}
			outline, err := ParseOutline(t.Context(), tc.source, format)
			if err != nil || !reflect.DeepEqual(full.Entries, outline.Entries) || !reflect.DeepEqual(full.Lines, outline.Lines) || len(outline.Tokens) != 0 {
				t.Fatalf("outline differs from complete parse: %+v, %v", outline, err)
			}
		})
	}
}

func BenchmarkStructuralParse(b *testing.B) {
	source := "package p\nfunc F() {\n" + strings.Repeat("println(value)\n", 10000) + "}\n"
	format, _ := sourcekind.Classify("a.go")
	for _, outline := range []bool{false, true} {
		name, parse := "full", Parse
		if outline {
			name, parse = "outline", ParseOutline
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := parse(b.Context(), source, format); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
