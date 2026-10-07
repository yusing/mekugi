package activity_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func TestLiveActivityMarkdownQuotes(t *testing.T) {
	p := activityui.Painter{}
	for _, tc := range []struct {
		name, text string
		want       []string
	}{
		{"paragraph", "> **Quoted** text\n> second line\nafter", []string{"▎ Quoted text", "▎ second line", "after"}},
		{"nested", "> outer\n> > inner", []string{"▎ outer", "▎ ▎ inner"}},
		{"blank", "> one\n>\n> two", []string{"▎ one", "▎ ", "▎ two"}},
		{"empty", ">", []string{"▎ "}},
		{"list", "> - first\n> - second", []string{"▎ • first", "▎ • second"}},
		{"quoted code", "> ```text\n> > literal\n> ```", []string{"▎  > literal" + strings.Repeat(" ", 28)}},
		{"code unchanged", "```text\n> literal\n```", []string{" > literal" + strings.Repeat(" ", 30)}},
		{"inline unchanged", "value > threshold", []string{"value > threshold"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := strings.Join(plainLines(p.Markdown(tc.text, 40)), "\n")
			if got != strings.Join(tc.want, "\n") {
				t.Fatalf("quote rendering: %q, want %q", got, strings.Join(tc.want, "\n"))
			}
		})
	}
	for _, width := range []int{1, 2, 12, 30} {
		rows := p.Markdown("> **Quoted words** that must wrap across multiple lines.", width)
		if len(rows) < 2 {
			t.Fatalf("quote did not wrap at width %d: %q", width, rows)
		}
		for _, row := range rows {
			if ansi.StringWidth(row) > width || !strings.HasPrefix(ansi.Strip(row), "▎") {
				t.Fatalf("quote rail or width lost at %d: %q", width, row)
			}
		}
	}
}

func TestUISnapshotMarkdownCodeFill(t *testing.T) {
	for _, tc := range []struct {
		name    string
		painter activityui.Painter
	}{
		{"terminal", activityui.Painter{}}, // Undetected theme, as behind mosh.
		{"dark", activityui.Painter{Theme: livediff.DarkTheme}},
		{"light", activityui.Painter{Theme: livediff.LightTheme}},
		{"reported_background", activityui.Painter{Theme: livediff.DarkTheme, Colors: activityui.Colors{Background: livediff.RGB{R: 40, G: 44, B: 52}, HasBackground: true}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := tc.painter
			rows := p.Markdown("```diff\n+added\n\n-removed that must wrap\n```", 12)
			rows = append(rows, p.Markdown("```\nx\n```", 8)...)
			rows = append(rows, p.Markdown("```go\nfmt.Println(x)\n```", 30)...)
			rows = append(rows, p.Markdown("> **bold**", 30)...)
			rows = append(rows, "plain after code")
			uisnapshot.AssertTerminal(t, "testdata/snapshots/markdown_code_fill_"+tc.name+".txt", rows, 30)
		})
	}
}

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
		theme livediff.Theme
	}{{"terminal", livediff.TerminalTheme}, {"dark", livediff.DarkTheme}, {"light", livediff.LightTheme}} {
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

func TestLiveActivityMarkdownTrimsOuterBlankLines(t *testing.T) {
	p := activityui.Painter{}
	got := plainLines(p.Markdown("\n\n\nFirst paragraph.\n\nSecond paragraph.\n\n", 40))
	want := []string{"First paragraph.", "", "Second paragraph."}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("outer blank lines kept: %q, want %q", got, want)
	}
	if rows := p.Markdown("\n\n", 40); len(rows) != 0 {
		t.Fatalf("blank message rows: %q", rows)
	}
}

func TestMarkdownMermaid(t *testing.T) {
	p := activityui.Painter{}
	source := "flowchart TD; A[Input] & B -- send --> C[Output]"
	text := "```mermaid\n" + source + "\n```"
	wide := strings.Join(plainLines(p.Markdown(text, 80)), "\n")
	if strings.Contains(wide, "flowchart") || !strings.Contains(wide, "Output") || !strings.Contains(wide, "◄") {
		t.Fatalf("diagram not rendered: %s", wide)
	}
	for _, tc := range []struct {
		text  string
		width int
	}{{text, 8}, {"```mermaid\n" + source, 80}, {"```mermaid\nflowchart TD; A -->\n```", 80}, {"```text\n" + source + "\n```", 80}} {
		got := strings.Join(plainLines(p.Markdown(tc.text, tc.width)), "\n")
		want := strings.Join(plainLines(p.Markdown(strings.Replace(tc.text, "```mermaid", "```text", 1), tc.width)), "\n")
		if got != want {
			t.Fatalf("source fallback lost: %q", got)
		}
	}
	quoted := strings.Join(plainLines(p.Markdown("> ```mermaid\n> graph;A-->B\n> ```", 80)), "\n")
	if !strings.Contains(quoted, "▎ ┌") || !strings.Contains(quoted, "◄") {
		t.Fatalf("quote diagram: %s", quoted)
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
