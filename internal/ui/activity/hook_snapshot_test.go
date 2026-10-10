package activity

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func TestUISnapshotHookRuns(t *testing.T) {
	for _, width := range []int{38, 80} {
		p := Painter{Theme: livediff.DarkTheme, Clock: func() time.Time { return time.Unix(1001, 0) }}
		var rows []string
		for _, status := range []string{"running", "completed", "failed", "blocked", "stopped", "unknown"} {
			b := Block{Kind: "op", Verb: "Run", Label: "PreToolUse · `hooks.toml` · async", Running: status == "running",
				Hook:    &HookDetails{HandlerType: "command", ExecutionMode: "async", Status: status},
				Started: time.Unix(1000, 0), Duration: 120 * time.Millisecond}
			if status == "completed" {
				b.Tail = []string{"context: #!/bin/bash", "echo ready"}
			}
			if status == "failed" {
				b.Tail = []string{"error: executable not found", "warning: check hook configuration"}
			}
			if status == "blocked" {
				b.Tail = []string{"feedback: operation rejected"}
			}
			if status == "unknown" {
				b.Label += " · outcome unknown"
				b.Duration = 0
			}
			rows = append(rows, p.Block(b, width)...)
			if status == "failed" {
				page := p.DialogPage(b, width-4)
				var body []string
				for i := range page.Lines {
					body = append(body, page.Rows(i, width-4)...)
				}
				rows = append(rows, p.Dialog(DialogFrame{Page: page, Rows: body, Total: len(body), Footer: "esc close"}, width, 10)...)
			}
		}
		uisnapshot.Assert(t, fmt.Sprintf("testdata/snapshots/hooks_%d.txt", width), strings.Join(rows, "\n")+"\n")
		uisnapshot.AssertTerminal(t, fmt.Sprintf("testdata/snapshots/hooks_styles_%d.txt", width), append(rows, "plain after hooks"), width)
	}
}
