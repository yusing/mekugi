package terminalui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestPaneBoxBodyWidth(t *testing.T) {
	for _, tc := range []struct{ body, padded string }{
		{"", "     "}, {"abc", "abc  "}, {"abcde", "abcde"}, {"abcdef", "abcde"},
		{"界界", "界界 "}, {"界界界", "界界 "}, {"e\u0301😀", "e\u0301😀  "},
		{"\x1b[31mabc\x1b[0m", "\x1b[31mabc\x1b[0m  "},
		{"\x1b[31m界界界\x1b[0m", "\x1b[31m界界\x1b[0m "},
	} {
		t.Run(tc.body, func(t *testing.T) {
			rows := PaneBox(7, 3, "", "", false, []string{tc.body}, nil)
			border := PaneBorder(false) + "│" + Reset
			if rows[1] != border+tc.padded+border || ansi.StringWidth(rows[1]) != 7 {
				t.Fatalf("unexpected padded row: %q", rows[1])
			}
		})
	}
}

func BenchmarkPaneBoxStyledRows(b *testing.B) {
	body := make([]string, 38)
	for i := range body {
		body[i] = "\x1b[32m+\x1b[0m " + strings.Repeat("const value = 42; ", 4)
	}
	b.ReportAllocs()
	for b.Loop() {
		PaneBox(86, 40, "Changes", "+123 -45", false, body, nil)
	}
}
