package activity

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func TestUISnapshotDialogSurfaceStyles(t *testing.T) {
	for _, tc := range []struct {
		name  string
		theme livediff.Theme
	}{
		{"terminal", livediff.TerminalTheme},
		{"dark", livediff.DarkTheme},
		{"light", livediff.LightTheme},
	} {
		for _, fallback := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/fallback=%v", tc.name, fallback), func(t *testing.T) {
				p := Painter{Theme: tc.theme}
				frame := DialogFrame{Page: DialogPage{Title: "Title", Detail: Dim + "metadata" + Undim}, Rows: []string{
					"A\x1b[0mB\x1b[mC\x1b[39;49mD",
					"\x1b[31mR\x1b[48;2;1;2;3mF\x1b[49mS",
				}, Footer: "esc close"}
				rows := p.Dialog(frame, 40, 8)
				if fallback {
					for i := range rows {
						rows[i] = FaintFallback(rows[i])
					}
				}
				rows = append(rows, "outside")
				path := fmt.Sprintf("testdata/snapshots/dialog_surface_%s_fallback_%t.txt", tc.name, fallback)
				uisnapshot.AssertTerminal(t, path, rows, 40)
			})
		}
	}
}

func TestDialogPageNumberingAndGutters(t *testing.T) {
	var p Painter
	page := p.DialogPage(Block{Verb: "Run", Code: "echo one\necho two", Tail: []string{"one", "two"}, TailOmitted: 9}, 80)
	var numbered []DialogLine
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
	read := p.DialogPage(Block{Kind: "reads", Verb: "Read", Reads: []Read{{Path: "sample.txt", Ranges: []string{"42:43"}}}, Tail: []string{"one", "two"}}, 80)
	if len(read.Lines) != 2 || read.Lines[0].Number != 42 || read.Lines[1].Number != 43 || read.Lines[0].Gutter != "│" {
		t.Fatalf("read lines = %+v", read.Lines)
	}
}

func TestDialogPageNotes(t *testing.T) {
	var p Painter
	for _, tc := range []struct {
		name   string
		output *Output
		want   string
	}{
		{"waiting", &Output{}, "Waiting for output…"},
		{"empty", &Output{done: true}, "No output"},
		{"released", &Output{done: true, released: true}, "released"},
		{"dropped", &Output{done: true, dropped: 3, lines: []string{"kept"}}, "3 earlier lines"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page := p.DialogPage(Block{Verb: "Run", Output: tc.output}, 100)
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
			if page.Live == tc.output.done {
				t.Fatal("incorrect live state")
			}
		})
	}
}

func TestDialogReleasedOutputUsesLastTail(t *testing.T) {
	var p Painter
	output := &Output{done: true, released: true}
	page := p.DialogPage(Block{Verb: "Run", Output: output, Tail: []string{"last visible line"}, TailOmitted: 4}, 80)
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

func TestDialogWrappedRowsMatchesWrap(t *testing.T) {
	for _, text := range []string{"", "abc def  ghi", "界界ab", "e\u0301👩‍💻text", "\x1b[31mcolored text\x1b[0m", "\x1b]8;;https://example.com\x1b\\linked text\x1b]8;;\x1b\\"} {
		for _, width := range []int{1, 2, 5, 20} {
			t.Run(fmt.Sprintf("%q/%d", text, width), func(t *testing.T) {
				if got, want := wrappedRows(text, width), len(Wrap(text, width, true)); got != want {
					t.Fatalf("rows=%d want %d", got, want)
				}
			})
		}
	}
	page := DialogPage{digits: 2, Lines: []DialogLine{{Number: 12, Gutter: "┆", Text: "abcdefgh"}}}
	rows := page.Rows(0, 9)
	if len(rows) != 2 || ansi.Strip(rows[0]) != "12 ┆ abcd" || ansi.Strip(rows[1]) != "   ┆ efgh" || page.Indent(0) != 5 || page.RowCount(0, 9) != 2 {
		t.Fatalf("wrapped numbered rows=%q", rows)
	}
}

func TestDialogFrameDimensions(t *testing.T) {
	var p Painter
	for _, width := range []int{12, 24, 80} {
		for _, height := range []int{5, 12} {
			frame := DialogFrame{Page: DialogPage{Title: strings.Repeat("title", 30), Detail: "detail", Live: true}, Position: "2 / 4", Paused: true, Rows: []string{strings.Repeat("界", 100), "short"}, Total: 100, Top: 50, Footer: "escape close · scroll"}
			rows := p.Dialog(frame, width, height)
			if len(rows) != height {
				t.Fatalf("height=%d want %d", len(rows), height)
			}
			for i, row := range rows {
				if got := ansi.StringWidth(row); got != width {
					t.Errorf("row %d width=%d want %d: %q", i, got, width, ansi.Strip(row))
				}
			}
		}
	}
	if p.Dialog(DialogFrame{}, 11, 10) != nil || p.Dialog(DialogFrame{}, 80, 4) != nil {
		t.Fatal("undersized frame rendered")
	}
}

func TestDialogCopiesShortSourceWithoutOutput(t *testing.T) {
	var p Painter
	page := p.DialogPage(Block{Verb: "Run", Code: "true", Output: &Output{done: true}}, 80)
	if page.Text != "true" {
		t.Fatalf("copy = %q", page.Text)
	}
}

func TestDialogDroppedReadKeepsRangeOffset(t *testing.T) {
	var p Painter
	page := p.DialogPage(Block{Kind: "reads", Verb: "Read", Reads: []Read{{Path: "sample.txt", Ranges: []string{"42:100"}}}, Output: &Output{done: true, dropped: 3, lines: []string{"kept"}}}, 80)
	for _, line := range page.Lines {
		if line.Gutter == "│" {
			if line.Number != 45 {
				t.Fatalf("read number = %d", line.Number)
			}
			return
		}
	}
	t.Fatal("read output absent")
}

func TestDialogReadSyntaxPreservesLines(t *testing.T) {
	for _, path := range []string{"sample.go", "sample.py", "sample.js"} {
		for _, trailingBlank := range []bool{false, true} {
			lines := []string{"return 42"}
			if trailingBlank {
				lines = append(lines, "")
			}
			var p Painter
			page := p.DialogPage(Block{Kind: "reads", Verb: "Read", Reads: []Read{{Path: path}}, Tail: lines}, 80)
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
		for _, theme := range []livediff.Theme{livediff.DarkTheme, livediff.LightTheme} {
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
	p := Painter{Theme: livediff.DarkTheme}
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

func TestBackdropFadesColorsAndKeepsCursorMoves(t *testing.T) {
	row := "\x1b[1G\x1b[0m\x1b[38;2;1;2;3mSearch\x1b[0m\x1b[12G\x1b[48;2;22;42;29m+ added\x1b[0m"
	if got, want := Backdrop(row), Dim+"\x1b[1GSearch\x1b[12G+ added"; got != want {
		t.Fatalf("Backdrop = %q, want %q", got, want)
	}
}

func TestDialogSkillReadMarkdown(t *testing.T) {
	content := "# Skill guide\n\nUse **careful steps** and `code`.\n\n- Read before editing and keep the output within the available width.\n\n```go\nreturn 42\n```\n"
	for _, theme := range []livediff.Theme{livediff.DarkTheme, livediff.LightTheme} {
		for _, width := range []int{24, 80} {
			for _, retained := range []bool{false, true} {
				p := Painter{Theme: theme}
				block := ParseOperation("Skill `example`")
				block.Tail = strings.Split(content, "\n")
				if retained {
					block.Output = &Output{done: true, lines: block.Tail}
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
	for _, tc := range []struct {
		name   string
		output *Output
		note   string
	}{
		{"live", &Output{lines: []string{"# Guide"}}, ""},
		{"dropped", &Output{done: true, dropped: 4, lines: []string{"# Guide"}}, "4 earlier lines"},
		{"released", &Output{done: true, released: true}, "released"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var p Painter
			page := p.DialogPage(Block{Kind: "reads", Verb: "Skill", Output: tc.output, Tail: []string{"# Guide"}}, 80)
			var rows []string
			for _, line := range page.Lines {
				rows = append(rows, ansi.Strip(line.Text))
			}
			if page.Text != "# Guide" || !strings.Contains(strings.Join(rows, "\n"), "Guide\n") || !strings.Contains(strings.Join(rows, "\n"), tc.note) || page.Live == tc.output.done {
				t.Fatalf("skill state lost: %+v", page)
			}
		})
	}
	var p Painter
	block := ParseOperation("Skill `run example/script.sh`")
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
	for _, theme := range []livediff.Theme{livediff.DarkTheme, livediff.LightTheme} {
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
