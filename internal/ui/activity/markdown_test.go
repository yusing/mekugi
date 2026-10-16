package activity_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/yusing/mekugi/libraries/diffrender"

	"github.com/charmbracelet/x/ansi"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func plainLines(lines []string) []string {
	plain := make([]string, len(lines))
	for i, line := range lines {
		plain[i] = ansi.Strip(line)
	}
	return plain
}

func TestUISnapshotFlashedAnswerCard(t *testing.T) {
	for _, tc := range []struct {
		name  string
		theme diffrender.Theme
	}{{"terminal", diffrender.TerminalTheme}, {"dark", diffrender.DarkTheme}, {"light", diffrender.LightTheme}} {
		t.Run(tc.name, func(t *testing.T) {
			p := activityui.Painter{Theme: tc.theme}
			block := activityui.Block{Kind: "final", Body: "The answer text."}
			rows := p.Event(block, 40)
			block.Flash = true
			rows = append(rows, p.Event(block, 40)...)
			rows = append(rows, "plain after flash")
			uisnapshot.AssertTerminal(t, fmt.Sprintf("testdata/snapshots/answer_flash_%s.txt", tc.name), rows, 40)
		})
	}
}

func TestMarkdownMermaidSummary(t *testing.T) {
	p := activityui.Painter{}
	for _, source := range []string{"```mermaid\nflowchart TD\nA --> B\n```", "> ```mermaid\n> flowchart TD\n> A --> B\n> ```", "```mermaid\nflowchart TD\nA -->"} {
		got := ansi.Strip(p.Summary([]activityui.Block{{Kind: "final", Body: source}}, 80))
		if !strings.Contains(got, "Mermaid: flowchart TD") || strings.ContainsAny(got, "┌─┐") {
			t.Fatalf("diagram summary: %q", got)
		}
	}
}
