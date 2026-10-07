package router

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

func TestJournalPreviewRetainsMarkdownSummary(t *testing.T) {
	for _, text := range []string{
		"**Finished** checking the [result](https://example.com).\n\nDetails follow.",
		"```go\n// First visible line\npackage main\nfunc main() {}\n```",
		"| Status | Result |\n| --- | --- |\n| done | passed |",
		"> Quoted result\n\n- next step",
		"\n\n  - Completed result\n\n```go\npackage main\n```",
		"",
		"#\nActual result",
		"** **\nActual result",
		"\x1b[0m\nActual result",
	} {
		t.Run(text, func(t *testing.T) {
			p := activityui.Painter{}
			want := p.Summary([]activityui.Block{{Kind: "final", Body: text}}, 80)
			if got := journalPreview(text); got != want {
				t.Fatalf("plain preview changed Markdown summary: got %q, want %q", got, want)
			}
			if got := strings.TrimSpace(ansi.Strip(journalInlinePreview(&p, text))); got != want {
				t.Fatalf("styled preview changed Markdown summary: got %q, want %q", got, want)
			}
		})
	}
}

func BenchmarkJournalPreview(b *testing.B) {
	text := "Completed the profiling change.\n\n```go\n" + strings.Repeat("fmt.Println(\"profile result\")\n", 200) + "```"
	b.ReportAllocs()
	for b.Loop() {
		journalPreview(text)
	}
}
