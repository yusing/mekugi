package terminalview

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/terminalui"
	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
)

func TestDialogPageNumberingAndGutters(t *testing.T) {
	var p Painter
	page := p.DialogPage(Block{Verb: "Run", Code: "echo one\necho two", Tail: []string{"one", "two"}, TailOmitted: 9}, 80)
	var numbered []terminalui.DialogLine
	for _, line := range page.Lines {
		if line.Number > 0 {
			numbered = append(numbered, line)
		}
	}
	if len(numbered) != 4 {
		t.Fatalf("numbered lines = %+v", numbered)
	}
	for i, want := range []int{1, 2, 10, 11} {
		if numbered[i].Number != want {
			t.Errorf("line %d number=%d want %d", i, numbered[i].Number, want)
		}
		gutter := "│"
		if i >= 2 {
			gutter = "┆"
		}
		if numbered[i].Gutter != gutter {
			t.Errorf("line %d gutter=%q", i, numbered[i].Gutter)
		}
	}
	if page.Text != "one\ntwo" {
		t.Fatalf("copy = %q", page.Text)
	}
	read := p.DialogPage(Block{Kind: "reads", Verb: "Read", Reads: []activitymodel.Read{{Path: "sample.txt", Ranges: []string{"42:43"}}}, Tail: []string{"one", "two"}}, 80)
	if len(read.Lines) != 2 || read.Lines[0].Number != 42 || read.Lines[1].Number != 43 || read.Lines[0].Gutter != "│" {
		t.Fatalf("read lines = %+v", read.Lines)
	}
}

func TestDialogPageNotes(t *testing.T) {
	var p Painter
	for _, tc := range []struct {
		name   string
		output *activitymodel.Output
		want   string
	}{
		{"waiting", &activitymodel.Output{}, "Waiting for output…"},
		{"empty", completedOutput(""), "No output"},
		{"released", releasedOutput(), "released"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page := p.DialogPage(Block{Verb: "Run", OutputSnapshot: new(tc.output.View())}, 100)
			var notes []string
			for _, line := range page.Lines {
				if line.Number == 0 {
					notes = append(notes, ansi.Strip(line.Text))
					if line.Gutter != "" {
						t.Fatal("note has gutter")
					}
				}
			}
			if !strings.Contains(strings.Join(notes, " "), tc.want) {
				t.Fatalf("notes=%q", notes)
			}
			if page.Live == tc.output.View().Done {
				t.Fatal("incorrect live state")
			}
		})
	}
}

func TestDialogReleasedOutputUsesLastTail(t *testing.T) {
	var p Painter
	output := releasedOutput()
	page := p.DialogPage(Block{Verb: "Run", OutputSnapshot: new(output.View()), Tail: []string{"last visible line"}, TailOmitted: 4}, 80)
	if !strings.Contains(page.Text, "last visible line") {
		t.Fatalf("released output lost tail: %+v", page)
	}
	found := false
	for _, line := range page.Lines {
		if line.Gutter == "┆" && ansi.Strip(line.Text) == "last visible line" {
			found = true
		}
	}
	if !found {
		t.Fatalf("released output rows lost tail: %+v", page.Lines)
	}
}

func TestDialogCopiesShortSourceWithoutOutput(t *testing.T) {
	var p Painter
	page := p.DialogPage(Block{Verb: "Run", Code: "true", OutputSnapshot: new(completedOutput("").View())}, 80)
	if page.Text != "true" {
		t.Fatalf("copy = %q", page.Text)
	}
}

func TestDialogDroppedReadKeepsRangeOffset(t *testing.T) {
	var p Painter
	output := overflowingOutput(t, "kept")
	view := output.View()
	page := p.DialogPage(Block{Kind: "reads", Verb: "Read", Reads: []activitymodel.Read{{Path: "sample.txt", Ranges: []string{"42:100"}}}, OutputSnapshot: new(output.View())}, 80)
	found, note := false, false
	for _, line := range page.Lines {
		if line.Number == 0 {
			if line.Gutter != "" {
				t.Fatal("note has gutter")
			}
			note = note || strings.Contains(ansi.Strip(line.Text), fmt.Sprintf("%d earlier lines", view.Dropped))
		}
		if line.Gutter == "│" && !found {
			if line.Number != 42+view.Dropped {
				t.Fatalf("read number = %d, dropped = %d", line.Number, view.Dropped)
			}
			found = true
		}
	}
	if !found || !note {
		t.Fatalf("read output or dropped note absent: output=%t note=%t", found, note)
	}
}

func TestDialogReadSyntaxPreservesLines(t *testing.T) {
	for _, path := range []string{"sample.go", "sample.py", "sample.js"} {
		for _, trailingBlank := range []bool{false, true} {
			lines := []string{"return 42"}
			if trailingBlank {
				lines = append(lines, "")
			}
			var p Painter
			page := p.DialogPage(Block{Kind: "reads", Verb: "Read", Reads: []activitymodel.Read{{Path: path}}, Tail: lines}, 80)
			if len(page.Lines) != len(lines) || page.Text != strings.Join(lines, "\n") {
				t.Fatalf("%s: changed retained content: %+v", path, page)
			}
			for i, line := range page.Lines {
				if ansi.Strip(line.Text) != lines[i] {
					t.Fatalf("%s: changed line %d: %q", path, i, line.Text)
				}
			}
			if page.Lines[0].Text == lines[0] {
				t.Errorf("%s blank=%v: syntax colors missing", path, trailingBlank)
			}
		}
	}
}

func TestDialogSourceTitleAndBodySyntax(t *testing.T) {
	for _, tc := range []struct{ verb, lang, code string }{
		{"Run", "", "echo \"hello\""},
		{"Run", "javascript", "const answer = 42;"},
		{"Run", "diff", "+added line"},
		{"MCP", "", `{"answer": 42}`},
	} {
		for _, theme := range []diffrender.Theme{diffrender.DarkTheme, diffrender.LightTheme} {
			p := Painter{Theme: theme}
			block := Block{Verb: tc.verb, Lang: tc.lang, Code: tc.code}
			page := p.DialogPage(block, 80)
			target := dialogTarget(&p, block)
			if ansi.Strip(target) != tc.code || target == tc.code || !strings.Contains(page.Title, target) {
				t.Fatalf("%s: title lost source or syntax: %q", tc.verb, page.Title)
			}
			block.Code += "\n\n"
			page = p.DialogPage(block, 80)
			if len(page.Lines) != 3 || page.Lines[0].Text == tc.code || ansi.Strip(page.Lines[0].Text) != tc.code || page.Text != block.Code {
				t.Fatalf("%s: body lost source or syntax: %+v", tc.verb, page)
			}
		}
	}
}

func TestDialogSearchTitleAndResultSyntax(t *testing.T) {
	p := Painter{Theme: diffrender.DarkTheme}
	for _, line := range []string{"main.go:12:return 42", "12:return 42"} {
		page := p.DialogPage(Block{Verb: "Search", Label: "`return` in `main.go`", Tail: []string{line}}, 80)
		if !strings.Contains(page.Title, p.Theme.Accent()+"return") {
			t.Fatalf("search title lost pattern color: %q", page.Title)
		}
		if len(page.Lines) != 1 || ansi.Strip(page.Lines[0].Text) != line || page.Text != line {
			t.Fatalf("search result changed: %+v", page)
		}
		if !strings.Contains(page.Lines[0].Text, p.Highlight("go", "return 42\n")[0]) || page.Lines[0].Text == line {
			t.Fatalf("search result lost code colors: %q", page.Lines[0].Text)
		}
	}
}

func TestDialogSkillReadMarkdown(t *testing.T) {
	content := "# Skill guide\n\nUse **careful steps** and `code`.\n\n- Read before editing and keep the output within the available width.\n\n```go\nreturn 42\n```\n"
	for _, theme := range []diffrender.Theme{diffrender.DarkTheme, diffrender.LightTheme} {
		for _, width := range []int{24, 80} {
			for _, retained := range []bool{false, true} {
				p := Painter{Theme: theme}
				block := Block{Content: activitymodel.ParseOperation("Skill `example`")}
				block.Tail = strings.Split(content, "\n")
				if retained {
					block.OutputSnapshot = new(completedOutput(content).View())
					block.Tail = []string{"stale tail"}
				}
				page := p.DialogPage(block, width)
				wantCopy := content
				if retained {
					// Retention removes trailing padding before the dialog sees it.
					wantCopy = strings.TrimRight(content, "\n")
				}
				if page.Text != wantCopy {
					t.Fatalf("copy changed Markdown source: %q", page.Text)
				}
				var rendered []string
				for i, line := range page.Lines {
					if line.Number != 0 || line.Gutter != "" || page.Indent(i) != 0 || page.RowCount(i, width) != 1 {
						t.Fatalf("Markdown row has source/output layout: %+v", line)
					}
					rows := page.Rows(i, width)
					if len(rows) != 1 || ansi.StringWidth(rows[0]) > width {
						t.Fatalf("Markdown row exceeds width %d: %q", width, rows)
					}
					rendered = append(rendered, rows[0])
				}
				plain := ansi.Strip(strings.Join(rendered, "\n"))
				if !strings.HasPrefix(plain, "Skill guide\n") || !strings.Contains(plain, "careful steps") || !strings.Contains(plain, "return 42") || strings.Contains(plain, "**") || strings.Contains(plain, "```") || strings.Contains(plain, "- Read") {
					t.Fatalf("unrendered Markdown: %q", plain)
				}
				if page.Lines[0].Text == "Skill guide" {
					t.Fatal("heading lost styling")
				}
			}
		}
	}
}

func TestDialogSkillOutputStates(t *testing.T) {
	live := new(activitymodel.Output)
	live.Write("# Guide")
	dropped := overflowingOutput(t, "# Guide")
	for _, tc := range []struct {
		name   string
		output *activitymodel.Output
		note   string
	}{
		{"live", live, ""},
		{"dropped", dropped, fmt.Sprintf("%d earlier lines", dropped.View().Dropped)},
		{"released", releasedOutput(), "released"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var p Painter
			page := p.DialogPage(Block{Kind: "reads", Verb: "Skill", OutputSnapshot: new(tc.output.View()), Tail: []string{"# Guide"}}, 80)
			var rows []string
			for _, line := range page.Lines {
				rows = append(rows, ansi.Strip(line.Text))
			}
			view := tc.output.View()
			want := strings.Join(view.Lines, "\n")
			if view.Released {
				want = "# Guide"
			}
			if page.Text != want || !strings.Contains(strings.Join(rows, "\n"), "Guide\n") || !strings.Contains(strings.Join(rows, "\n"), tc.note) || page.Live == tc.output.View().Done {
				t.Fatalf("skill state %s lost: live=%t, copied bytes=%d", tc.name, page.Live, len(page.Text))
			}
		})
	}
	var p Painter
	block := Block{Content: activitymodel.ParseOperation("Skill `run example/script.sh`")}
	block.Tail = []string{"# literal", "**output**"}
	page := p.DialogPage(block, 80)
	for i, line := range page.Lines {
		if line.Number != i+1 || line.Gutter != "┆" || line.Text != block.Tail[i] {
			t.Fatalf("skill script output no longer literal: %+v", page)
		}
	}
}

func TestRunElapsedSuffix(t *testing.T) {
	now := time.Unix(100, 0)
	for _, tc := range []struct {
		duration time.Duration
		want     string
	}{
		{0, ""}, {3 * time.Millisecond, ""}, {3500 * time.Microsecond, "3ms"}, {4 * time.Millisecond, "4ms"},
		{999 * time.Millisecond, "999ms"}, {time.Second, "1s"}, {70 * time.Second, "1m10s"}, {time.Hour, "1h"},
	} {
		for _, live := range []bool{false, true} {
			b := Block{Kind: "op", Verb: "Run", Code: "echo ok", Duration: tc.duration, Running: live}
			if live {
				b.Started = now.Add(-tc.duration)
			}
			if got := RunElapsed(b, now); got != tc.want {
				t.Fatalf("%s live=%v: %q", tc.duration, live, got)
			}
			p := Painter{}
			title := ansi.Strip(p.DialogPageTitle(b, now, 80))
			if tc.want != "" && !strings.HasSuffix(title, " · "+tc.want) {
				t.Fatalf("title %q", title)
			}
			if !live {
				row := ansi.Strip(strings.Join(p.Block(b, 100), "\n"))
				if tc.want != "" && !strings.Contains(row, " · "+tc.want) {
					t.Fatalf("row %q", row)
				}
			}
		}
	}
}

func TestDialogMchangesDiffColorsAndCopy(t *testing.T) {
	source := "amber1\n--- a.go\n+++ a.go\n@@ -1 +1 @@\n-old\n+new"
	for _, theme := range []diffrender.Theme{diffrender.DarkTheme, diffrender.LightTheme} {
		p := Painter{Theme: theme}
		page := p.DialogPage(Block{Kind: "op", Verb: "Run", Code: "mchanges amber1", Tail: strings.Split(source, "\n")}, 80)
		if page.Text != source {
			t.Fatal("copy changed")
		}
		colored := map[string]bool{}
		for _, row := range page.Lines {
			if row.Gutter == "┆" {
				colored[ansi.Strip(row.Text)] = row.Text != ansi.Strip(row.Text)
			}
		}
		for _, line := range []string{"-old", "+new", "@@ -1 +1 @@"} {
			if !colored[line] {
				t.Fatalf("uncolored %q", line)
			}
		}
	}
}

func TestDialogNarrativeCopiesMarkdown(t *testing.T) {
	p := Painter{}
	page := p.DialogPage(Block{Kind: "summary", Body: "**Full** narrative\n\nLast paragraph"}, 40)
	if page.Text != "**Full** narrative\n\nLast paragraph" || strings.Contains(page.Text, "\x1b") {
		t.Fatalf("copy %q", page.Text)
	}
	if len(page.Lines) == 0 {
		t.Fatalf("page %+v", page)
	}
}

func completedOutput(text string) *activitymodel.Output {
	output := new(activitymodel.Output)
	output.Finish(new(text), nil)
	return output
}

func releasedOutput() *activitymodel.Output {
	output := new(activitymodel.Output)
	output.Release()
	return output
}

func overflowingOutput(t *testing.T, last string) *activitymodel.Output {
	t.Helper()
	output := new(activitymodel.Output)
	line := strings.Repeat("x", activitymodel.OutputLineBytes)
	output.Write(strings.Repeat(line+"\n", activitymodel.OutputBytes/activitymodel.OutputLineBytes+3))
	output.Write(last + "\n")
	output.Finish(nil, nil)
	if output.View().Dropped == 0 {
		t.Fatal("fixture did not exceed output retention")
	}
	return output
}
