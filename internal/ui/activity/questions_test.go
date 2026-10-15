package activity

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/testdata/uisnapshot"
)

func TestQuestionBlockRendersFullAnswerAtNarrowWidthsInBothThemes(t *testing.T) {
	question := "Should customers receive the complete launch update, including the rollout schedule?"
	answer := "Approved: send only to beta users on Friday."
	for _, theme := range []livediff.Theme{livediff.DarkTheme, livediff.LightTheme} {
		p := Painter{Theme: theme}
		for _, width := range []int{18, 24, 80} {
			block := Block{Kind: "op", Verb: "Asked", Label: "1 question · answered", Questions: []Question{{Text: question, Answer: answer, State: "answered"}}}
			rows := p.Block(block, width)
			var plain strings.Builder
			for _, row := range rows {
				if ansi.StringWidth(row) > width {
					t.Fatalf("theme=%v width=%d overflow: %q", theme, width, row)
				}
				plain.WriteString(ansi.Strip(row))
				plain.WriteByte(' ')
			}
			got := strings.Join(strings.Fields(plain.String()), " ")
			if !strings.Contains(got, answer) {
				t.Fatalf("theme=%v width=%d missing full answer %q in %q", theme, width, answer, got)
			}
			if !strings.Contains(got, question) {
				t.Fatalf("theme=%v width=%d missing full question %q in %q", theme, width, question, got)
			}
		}
	}
}

func TestUISnapshotQuestionMarkdown(t *testing.T) {
	for _, theme := range []livediff.Theme{livediff.DarkTheme, livediff.LightTheme} {
		p := Painter{Theme: theme, CopySource: true}
		answer := "> **Recommended next step:** check `host`.\n\n[Selected message] **This?**\n\n- Keep state\n\n```go\ncheck()\n```"
		rows := p.Block(Block{Kind: "op", Verb: "Asked", Label: "1 question · answered", Questions: []Question{{Text: "Proceed?", Answer: answer, State: "answered"}}}, 42)
		var parts []CopyFragment
		for i, row := range rows {
			plain, spans := ExtractCopy(row)
			rows[i] = plain
			for _, span := range spans {
				if part, ok := span.Clip(0, 42); ok {
					parts = append(parts, part)
				}
			}
		}
		if got, want := CopyText(parts), selectedMarkdown(t, answer, 38); got != want {
			t.Fatalf("answer copy = %q, want %q", got, want)
		}
		uisnapshot.AssertTerminal(t, fmt.Sprintf("testdata/snapshots/question_markdown_%d.txt", theme), append(rows, "plain after answer"), 42)
	}
}
