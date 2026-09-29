package activity

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func TestActivityTimingVisibleForRunAndClassifiedRead(t *testing.T) {
	started := time.Date(2026, 9, 30, 10, 11, 12, 123000000, time.Local)
	ended := started.Add(125 * time.Millisecond)
	for _, tc := range []struct {
		name  string
		block Block
	}{
		{"run", Block{Kind: "op", Verb: "Run", Code: "sleep .125", Started: started, Ended: ended, Duration: 125 * time.Millisecond}},
		{"read", Block{Kind: "reads", Verb: "Read", Reads: []Read{{Path: "sample.txt"}}, Code: "cat sample.txt", Started: started, Ended: ended, Duration: 125 * time.Millisecond}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var p Painter
			for _, width := range []int{60, 80} {
				page := p.DialogPage(tc.block, width)
				title := ansi.Strip(page.Title)
				var lines []string
				for _, line := range page.Lines {
					lines = append(lines, ansi.Strip(line.Text))
				}
				metadata := strings.Join(lines, "\n")
				if !strings.Contains(title, "125ms") || !strings.Contains(metadata, "Started "+started.Format("2006-01-02 15:04:05.000 MST")) || !strings.Contains(metadata, "Ended   "+ended.Format("2006-01-02 15:04:05.000 MST")) || !strings.Contains(metadata, "Elapsed 125ms") {
					t.Fatalf("width=%d title=%q metadata=%q", width, title, metadata)
				}
				if strings.Contains(page.Text, "Started ") || strings.Contains(page.Text, "Elapsed ") {
					t.Fatalf("metadata contaminated copy text: %q", page.Text)
				}
				var body []string
				for i := range page.Lines {
					body = append(body, page.Rows(i, width-6)...)
				}
				frame := DialogFrame{Page: page, Rows: body, Total: len(body), Footer: "escape close"}
				rendered := p.Dialog(frame, width, max(12, len(frame.Rows)+6))
				screen := ansi.Strip(strings.Join(rendered, "\n"))
				for _, want := range []string{"Started " + started.Format("2006-01-02 15:04:05.000 MST"), "Ended   " + ended.Format("2006-01-02 15:04:05.000 MST"), "Elapsed 125ms"} {
					if !strings.Contains(screen, want) {
						t.Fatalf("dialog clipped %q at %d: %s", want, width, screen)
					}
				}
				feed := ansi.Strip(strings.Join(p.Block(tc.block, width), "\n"))
				if !strings.Contains(feed, "125ms") {
					t.Fatalf("feed elapsed missing at %d: %q", width, feed)
				}
			}
		})
	}
}
