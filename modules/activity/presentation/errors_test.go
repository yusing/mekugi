package presentation

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestErrorPreview(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
	}{
		{"first_nonempty", "\n \t\n  first failure  \nprivate diagnostic", "first failure"},
		{"controls", "\x1b[31mfailed\x1b[0m\x07\r\trequest\x1b]0;injected title\x07\nsecond", "failed    request"},
		{"empty", "\n\t\x1b[2J\n", ""},
		{"exact_limit", strings.Repeat("a", 240), strings.Repeat("a", 240)},
		{"ascii_clipped", strings.Repeat("a", 241), strings.Repeat("a", 240) + "…"},
		{"partial_rune", strings.Repeat("a", 239) + "界extra", strings.Repeat("a", 239) + "…"},
		{"complete_rune", strings.Repeat("a", 237) + "界extra", strings.Repeat("a", 237) + "界…"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ErrorPreview(tc.body)
			if got != tc.want {
				t.Fatalf("preview = %q, want %q", got, tc.want)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("preview is not valid UTF-8: %q", got)
			}
		})
	}
}
