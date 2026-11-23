package terminalview

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/terminalui"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func TestQuestionBlockRendersFullAnswerAtNarrowWidthsInBothThemes(t *testing.T) {
	question := "Should customers receive the complete launch update, including the rollout schedule?"
	answer := "Approved: send only to beta users on Friday."
	for _, theme := range []diffrender.Theme{diffrender.DarkTheme, diffrender.LightTheme} {
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

func TestQuestionBlockStatePlacement(t *testing.T) {
	p := Painter{Theme: diffrender.DarkTheme}
	uniform := ansi.Strip(strings.Join(p.Block(Block{
		Kind:  "op",
		Verb:  "Asked",
		Label: "2 questions · answered",
		Questions: []Question{
			{Text: "Who should receive the release update?", Answer: "Internal team\n\nabc", State: "answered"},
			{Text: "When should the update go out?", Answer: "After validation", State: "answered"},
		},
	}, 80), "\n"))
	if strings.Count(uniform, "answered") != 1 {
		t.Fatalf("expected answered only in header, got:\n%s", uniform)
	}
	if !strings.Contains(uniform, "  Who should receive the release update?\n  └ Internal team") {
		t.Fatalf("answer did not follow question directly:\n%s", uniform)
	}

	mixed := ansi.Strip(strings.Join(p.Block(Block{
		Kind:  "op",
		Verb:  "Asked",
		Label: "2 questions · waiting",
		Questions: []Question{
			{Text: "Who receives the update?", Answer: "Beta users", State: "answered"},
			{Text: "When should it go out?", Options: "Monday · Friday", State: "waiting"},
		},
	}, 80), "\n"))
	if !strings.Contains(mixed, "  Who receives the update? · answered\n  └ Beta users") {
		t.Fatalf("expected inline mixed state on wide question row, got:\n%s", mixed)
	}
	if !strings.Contains(mixed, "  When should it go out? · waiting\n  Monday · Friday") {
		t.Fatalf("expected inline waiting state on wide question row, got:\n%s", mixed)
	}
}

func TestUISnapshotQuestionMarkdown(t *testing.T) {
	for _, theme := range []diffrender.Theme{diffrender.DarkTheme, diffrender.LightTheme} {
		p := Painter{Theme: theme, CopySource: true}
		answer := "> **Recommended next step:** check `host`.\n\n[Selected message] **This?**\n\n- Keep state\n\n```go\ncheck()\n```"
		rows := p.Block(Block{Kind: "op", Verb: "Asked", Label: "1 question · answered", Questions: []Question{{Text: "Proceed?", Answer: answer, State: "answered"}}}, 42)
		var parts []terminalui.CopyFragment
		for i, row := range rows {
			plain, spans := terminalui.ExtractCopy(row)
			rows[i] = plain
			for _, span := range spans {
				if part, ok := span.Clip(0, 42); ok {
					parts = append(parts, part)
				}
			}
		}
		want := "**Recommended next step:** check `host`.\n\n[Selected message] **This?**\n\n- Keep state\n\ncheck()"
		if got := terminalui.CopyText(parts); got != want {
			t.Fatalf("answer copy = %q, want %q", got, want)
		}
		uisnapshot.AssertTerminal(t, fmt.Sprintf("testdata/snapshots/question_markdown_%d.txt", theme), append(rows, "plain after answer"), 42)
	}
}
