package router

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestLiveActivityMarkdownQuotes(t *testing.T) {
	p := liveActivityPainter{}
	for _, tc := range []struct {
		name, text string
		want       []string
	}{
		{"paragraph", "> **Quoted** text\n> second line\nafter", []string{"│ Quoted text", "│ second line", "after"}},
		{"nested", "> outer\n> > inner", []string{"│ outer", "│ │ inner"}},
		{"blank", "> one\n>\n> two", []string{"│ one", "│ ", "│ two"}},
		{"empty", ">", []string{"│ "}},
		{"list", "> - first\n> - second", []string{"│ • first", "│ • second"}},
		{"quoted code", "> ```text\n> > literal\n> ```", []string{"│ │ > literal"}},
		{"code unchanged", "```text\n> literal\n```", []string{"│ > literal"}},
		{"inline unchanged", "value > threshold", []string{"value > threshold"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := strings.Join(plainLines(p.markdown(tc.text, 40)), "\n")
			if got != strings.Join(tc.want, "\n") {
				t.Fatalf("quote rendering: %q, want %q", got, strings.Join(tc.want, "\n"))
			}
		})
	}
	for _, width := range []int{1, 2, 12, 30} {
		rows := p.markdown("> **Quoted words** that must wrap across multiple lines.", width)
		if len(rows) < 2 {
			t.Fatalf("quote did not wrap at width %d: %q", width, rows)
		}
		for _, row := range rows {
			if ansi.StringWidth(row) > width || !strings.HasPrefix(ansi.Strip(row), "│") {
				t.Fatalf("quote rail or width lost at %d: %q", width, row)
			}
		}
	}
	if got := strings.Join(p.markdown("> **bold**", 30), "\n"); !strings.Contains(got, "\x1b[1mbold") {
		t.Fatalf("quote lost inline styling: %q", got)
	}
}
