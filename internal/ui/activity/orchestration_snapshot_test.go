package activity

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func TestUISnapshotOrchestrationEventStyles(t *testing.T) {
	for _, tc := range []struct {
		name  string
		theme livediff.Theme
	}{{"terminal", livediff.TerminalTheme}, {"dark", livediff.DarkTheme}, {"light", livediff.LightTheme}} {
		p := Painter{Theme: tc.theme}
		var rows []string
		for _, block := range []Block{
			{Kind: "start", Label: "model", Body: "Check the output."},
			{Kind: "message", From: "/root", To: "/root/batch", Body: "Check the output."},
			{Kind: "op", Verb: "Prepare", Label: "batch · completed"},
			{Kind: "progress", Body: "Finished waiting"},
			{Kind: "error", Body: "Start batch: permissions unavailable"},
		} {
			native := p.Event(block, 60)
			block.Orchestrated = true
			orchestrated := p.Event(block, 60)
			a, b := strings.Join(native, "\n"), strings.Join(orchestrated, "\n")
			if a == b || ansi.Strip(a) != ansi.Strip(b) {
				t.Fatal("orchestration must differ only in styling", block.Kind)
			}
			rows = append(rows, native...)
			rows = append(rows, orchestrated...)
			rows = append(rows, "plain after event")
		}
		uisnapshot.AssertTerminal(t, "testdata/snapshots/orchestration-styles-"+tc.name+".txt", rows, 60)
	}
}
