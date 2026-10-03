package activity

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
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
