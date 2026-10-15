package structure

import "testing"

// Source: internal/router/toolplugin/tests/javascript_string.test.ts
// Lone-surrogate names have separate lossless JSON coverage in the sourceoutline command acceptance.
func TestNativeDecodeJSStringValidEscapes(t *testing.T) {
	for _, quote := range []string{"'", "\""} {
		for _, tc := range []struct{ body, want string }{
			{"", ""}, {"side-effect", "side-effect"}, {"@scope/package", "@scope/package"}, {"日本語/😀", "日本語/😀"},
			{`\'\"\\`, "'\"\\"}, {`\b\f\n\r\t\v`, "\b\f\n\r\t\v"}, {`\0`, "\x00"}, {`\0a`, "\x00a"},
			{`\a\c\d\e\q\z\X\U\$\/\{\}`, "acdeqzXU$/{}"}, {`\x00\x41\xAf`, "\x00A¯"}, {`\u0000\u0041\u00aF`, "\x00A¯"},
			{`\uD83D\uDE00`, "😀"}, {`\uD800\uDC00`, "𐀀"}, {`\u{1F600}\u{10FFFF}`, "😀\U0010ffff"}, {`\u{0000000000000000000041}`, "A"},
			{"a\\\nb", "ab"}, {"a\\\rb", "ab"}, {"a\\\r\nb", "ab"}, {"a\\\u2028b\\\u2029c", "abc"}, {"a\u2028b\u2029c", "a\u2028b\u2029c"}, {"a\x00\tb", "a\x00\tb"}, {"\\😀", "😀"},
		} {
			literal := quote + tc.body + quote
			t.Run(literal, func(t *testing.T) {
				got, ok := decodeJSString(literal)
				if !ok || got != tc.want {
					t.Fatalf("decode %q = (%q,%v), want %q", literal, got, ok, tc.want)
				}
			})
		}
	}
}

func TestNativeDecodeJSStringInvalidEscapes(t *testing.T) {
	for _, literal := range []string{
		"", "'", "\"", "unquoted", "`template`", "'unclosed", "\"mismatched'", "'a'b'", "\"a\"b\"", "'ok' tail", " 'ok'", "'trailing\\'",
		"'raw\nnewline'", "'raw\rcarriage'", "'a\\\n\rb'", `'\00'`, `'\01'`, `'\07'`, `'\08'`, `'\09'`, `'\1'`, `'\7'`, `'\8'`, `'\9'`,
		`'\x'`, `'\x1'`, `'\xgg'`, `'\u'`, `'\u123'`, `'\uzzzz'`, `'\u{}'`, `'\u{1'`, `'\u{110000}'`, `'\u{1_0000}'`, `'\u{+1}'`, `'\u{ 1}'`, `'\u{0x41}'`,
	} {
		t.Run(literal, func(t *testing.T) {
			if got, ok := decodeJSString(literal); ok {
				t.Fatalf("invalid literal %q accepted as %q", literal, got)
			}
		})
	}
}
