package shellsyntax

import (
	"os/exec"
	"testing"
)

func TestQuoteSingleQuoteForm(t *testing.T) {
	for value, want := range map[string]string{
		"":     "''",
		"word": "'word'",
		"a'b":  `'a'\''b'`,
	} {
		if got := Quote(value); got != want {
			t.Errorf("Quote(%q) = %q, want %q", value, got, want)
		}
	}
}

func TestQuoteShellRoundTrip(t *testing.T) {
	for _, test := range []struct {
		name  string
		value string
	}{
		{"empty", ""},
		{"apostrophe", "it's 'quoted'"},
		{"newline", "first\nsecond\n"},
		{"command substitution", "$(printf expanded)"},
		{"backtick", "`printf expanded`"},
		{"spaces", "  two words\t "},
		{"backslash", `a\b\\c`},
		{"UTF8", "你好 café 🐈"},
	} {
		t.Run(test.name, func(t *testing.T) {
			quoted := Quote(test.value)
			// Count arguments as well as comparing output: an empty or split
			// argument can otherwise accidentally pass a printf-only check.
			command := exec.CommandContext(t.Context(), "sh", "-c", "set -- "+quoted+"; [ \"$#\" -eq 1 ] || exit 91; printf '%s' \"$1\"")
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("shell rejected Quote(%q) = %q: %v\n%s", test.value, quoted, err, output)
			}
			if string(output) != test.value {
				t.Fatalf("Quote(%q) = %q round trip = %q", test.value, quoted, output)
			}
		})
	}
}
