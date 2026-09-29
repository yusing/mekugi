package activity_test

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
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
	if got := strings.Join(p.Markdown("> **bold**", 30), "\n"); !strings.Contains(got, "\x1b[1mbold") {
		t.Fatalf("quote lost inline styling: %q", got)
	}
}

func TestLiveActivityMarkdownCodeFill(t *testing.T) {
	for _, p := range []activityui.Painter{
		{}, // Undetected theme, as behind mosh.
		{Theme: livediff.DarkTheme},
		{Theme: livediff.LightTheme},
		{Theme: livediff.DarkTheme, Colors: activityui.Colors{Background: livediff.RGB{R: 40, G: 44, B: 52}, HasBackground: true}},
	} {
		rows := p.Markdown("```diff\n+added\n\n-removed that must wrap\n```", 12)
		if len(rows) != 5 {
			t.Fatalf("code rows: %q", rows)
		}
		for _, row := range rows {
			if !strings.HasPrefix(row, "\x1b[48;2;") || ansi.StringWidth(row) != 12 || strings.Contains(ansi.Strip(row), "▎") {
				t.Fatalf("code row lost its fill or width: %q", row)
			}
			// Highlight resets must not end the fill before the row does.
			if _, after, ok := strings.Cut(row, activityui.Reset); ok && !strings.HasPrefix(after, "\x1b[48;2;") {
				t.Fatalf("reset cleared code fill: %q", row)
			}
		}
	}
	if got := (&activityui.Painter{Colors: activityui.Colors{Background: livediff.RGB{R: 40, G: 44, B: 52}, HasBackground: true}, Theme: livediff.DarkTheme}).Markdown("```\nx\n```", 8)[0]; !strings.HasPrefix(got, "\x1b[48;2;55;59;66m") {
		t.Fatalf("fill does not follow the reported background: %q", got)
	}
	// Without a detected theme the block carries its own foreground, so plain
	// text stays readable on the dark fill in a light terminal.
	const ink = "\x1b[38;2;230;237;243m"
	for _, row := range (&activityui.Painter{}).Markdown("```go\nfmt.Println(x)\n```", 30) {
		if !strings.HasPrefix(row, "\x1b[48;2;32;35;40m"+ink) || strings.Contains(strings.TrimSuffix(row, "\x1b[39m"), "\x1b[39m") || !strings.HasSuffix(row, "\x1b[49m\x1b[39m") {
			t.Fatalf("undetected theme code row lost its fill or foreground: %q", row)
		}
	}
}

func plainLines(lines []string) []string {
	plain := make([]string, len(lines))
	for i, line := range lines {
		plain[i] = ansi.Strip(line)
	}
	return plain
}

func TestFlashedAnswerCardGlowsAndLightsText(t *testing.T) {
	p := activityui.Painter{Theme: livediff.DarkTheme}
	block := activityui.Block{Kind: "final", Body: "The answer text."}
	plain := p.Event(block, 40)
	block.Flash = true
	lines := p.Event(block, 40)
	fill := p.Theme.SelectionBackground()
	if len(lines) != 3 || len(plain) != 3 {
		t.Fatalf("card rows: %q", lines)
	}
	if strings.Contains(lines[0], fill) || !strings.Contains(lines[1], fill) || strings.Contains(lines[2], fill) {
		t.Fatalf("flash must light the answer text only: %q", lines)
	}
	for k := range lines {
		if ansi.Strip(lines[k]) != ansi.Strip(plain[k]) || lines[k] == plain[k] {
			t.Fatalf("row %d did not glow in place: %q, plain %q", k, lines[k], plain[k])
		}
	}
	if !strings.HasPrefix(ansi.Strip(lines[1]), "│ The answer text.") {
		t.Fatalf("answer text moved: %q", ansi.Strip(lines[1]))
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
		got := ansi.Strip(p.Summary([]activityui.Block{{Kind: "final", Body: source}}))
		if !strings.Contains(got, "Mermaid: flowchart TD") || strings.ContainsAny(got, "┌─┐") {
			t.Fatalf("diagram summary: %q", got)
		}
	}
}
