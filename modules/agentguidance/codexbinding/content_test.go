package codexbinding

import (
	"strings"
	"testing"
)

func TestUnchangedTextContentPreservesBytes(t *testing.T) {
	for _, raw := range []string{` "\u0061" `, ` [ { "text" : "\u0061", "type":"input_text", "extension":9007199254740993 } ] `} {
		got, changed, err := transformResponsesTextContent([]byte(raw), func(s string) string { return s }, isResponsesInputTextPart)
		if err != nil || changed || string(got) != raw {
			t.Fatalf("unchanged carrier rewritten: %s, changed=%t, err=%v", got, changed, err)
		}
	}
}

func BenchmarkUnchangedTextContent(b *testing.B) {
	for _, array := range []bool{false, true} {
		name, raw := "string", `"`+strings.Repeat("x", 1<<20)+`"`
		if array {
			name, raw = "array", `[{"type":"input_text","text":`+raw+`}]`
		}
		b.Run(name, func(b *testing.B) {
			input := []byte(raw)
			want, _, err := transformResponsesTextContent(input, func(s string) string { return s }, isResponsesInputTextPart)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			for b.Loop() {
				got, _, err := transformResponsesTextContent(input, func(s string) string { return s }, isResponsesInputTextPart)
				if err != nil || string(got) != string(want) {
					b.Fatal("unchanged content changed", err)
				}
			}
		})
	}
}
