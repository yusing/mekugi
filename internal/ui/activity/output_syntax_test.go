package activity

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func TestCommandOutputAutoHighlightLimit(t *testing.T) {
	for _, theme := range []livediff.Theme{livediff.DarkTheme, livediff.LightTheme} {
		for _, size := range []int{outputAutoHighlightBytes, outputAutoHighlightBytes + 1} {
			source := "var answer = 42 // "
			source += strings.Repeat("x", size-len(source))
			rows := []string{source}
			p := Painter{Theme: theme}
			got := p.outputColors(Block{Verb: "Run"}, rows)
			if ansi.Strip(got[0]) != source || (got[0] != source) != (size <= outputAutoHighlightBytes) {
				t.Fatalf("size %d: text or color boundary changed", size)
			}
		}
		rows := strings.Split(strings.Repeat("var answer = 42\n", 700), "\n")
		block := selectedOutputBlock(Block{Verb: "Run"}, rows, len(rows)-3)
		p := Painter{Theme: theme}
		if got := p.tailColors(block); !slices.Equal(got, block.Tail) {
			t.Fatal("small tail bypassed retained output bound")
		}
		page := p.DialogPage(block, 80)
		if page.Text != strings.Join(block.Output.View().Lines, "\n") {
			t.Fatal("large output lost copyable text")
		}
		for _, row := range page.Lines {
			if row.Gutter == "┆" && ansi.Strip(row.Text) != row.Text {
				t.Fatal("dialog bypassed automatic syntax bound")
			}
		}
		for _, hinted := range []Block{
			{Kind: "reads", Verb: "Read", Reads: []Read{{Path: "file.go"}}},
			{Verb: "Search", Label: "Search in `file.go`"},
		} {
			hintedRows := rows
			if hinted.Verb == "Search" {
				hintedRows = make([]string, len(rows))
				for i, row := range rows {
					hintedRows[i] = fmt.Sprintf("%d:%s", i+1, row)
				}
			}
			if got := p.outputColors(hinted, hintedRows); slices.Equal(got, hintedRows) {
				t.Fatalf("explicit %s hint lost above automatic bound", hinted.Verb)
			}
		}
		diff := "--- a/file.go\n+++ b/file.go\n@@ -0,0 +1,700 @@\n" + strings.Repeat("+var answer = 42\n", 700)
		for _, block := range []Block{{Verb: "Run"}, {Verb: "Diff", Code: "git diff"}} {
			rows := strings.Split(diff, "\n")
			if got := p.outputColors(block, rows); slices.Equal(got, rows) {
				t.Fatal("diff colors lost above automatic bound")
			}
		}
	}
}

func TestUISnapshotCommandOutputAutoHighlightLimit(t *testing.T) {
	rows := strings.Split(strings.Repeat("var answer = 42\n", 700)+"completed successfully", "\n")
	block := selectedOutputBlock(Block{Kind: "op", Verb: "Run", Code: "custom-producer", Running: true}, rows, len(rows)-3)
	p := Painter{Theme: livediff.DarkTheme}
	uisnapshot.Assert(t, "testdata/snapshots/output_auto_highlight_limit.txt", strings.Join(p.Block(block, 80), "\n")+"\n")
}

func BenchmarkCommandOutputAutoHighlight(b *testing.B) {
	for _, size := range []int{8 << 10, 16 << 10, 64 << 10} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			rows := strings.Split(strings.Repeat("var answer = 42\n", size/16+1)[:size], "\n")
			b.ReportAllocs()
			for b.Loop() {
				p := Painter{Theme: livediff.DarkTheme}
				p.outputColors(Block{Verb: "Run"}, rows)
			}
		})
	}
}

func TestCommandOutputSyntaxIndependentOfProducer(t *testing.T) {
	for _, theme := range []livediff.Theme{livediff.DarkTheme, livediff.LightTheme} {
		p := Painter{Theme: theme}
		for _, tc := range []struct {
			name, source string
			colored      bool
		}{
			{"go", "package main\n\nfunc main() { println(42) }\n", true},
			{"shell", "#!/bin/bash\necho \"hello\"\n", true},
			{"diff", "--- a/file\n+++ b/file\n@@ -1 +1 @@\n-old\n+new\n", true},
			{"mchanges", "amber1\n--- a/file\n+++ b/file\n@@ -1 +1 @@\n-old\n+new", true},
			{"unknown", "completed successfully\nplain output", false},
		} {
			for _, command := range []string{"git diff", "mchanges amber1", "custom-producer"} {
				t.Run(tc.name+"/"+command, func(t *testing.T) {
					rows := strings.Split(tc.source, "\n")
					block := Block{Kind: "op", Verb: "Run", Code: command, Lang: "bash", Tail: rows}
					page := p.DialogPage(block, 120)
					if page.Text != tc.source {
						t.Fatalf("copy changed: %q", page.Text)
					}
					var plain, colored []string
					for _, line := range page.Lines {
						if line.Gutter == "┆" {
							if line.Number != len(plain)+1 {
								t.Fatalf("output line number %d", line.Number)
							}
							plain = append(plain, ansi.Strip(line.Text))
							colored = append(colored, line.Text)
						}
					}
					if strings.Join(plain, "\n") != tc.source {
						t.Fatalf("display changed: %q", plain)
					}
					if (strings.Join(colored, "\n") != tc.source) != tc.colored {
						t.Fatalf("unexpected syntax colors: %q", colored)
					}
					inline := strings.Join(p.Block(block, 120), "\n")
					for _, row := range colored {
						if row != "" && !strings.Contains(inline, row) {
							t.Fatalf("inline and dialog colors differ for %q", row)
						}
					}
					if strings.Join(block.Tail, "\n") != tc.source {
						t.Fatal("render mutated retained output")
					}
				})
			}
		}
	}
}

func TestCommandOutputSyntaxBoundAndExplicitHint(t *testing.T) {
	p := Painter{Theme: livediff.DarkTheme}
	source := "package main\n" + strings.Repeat("x", dialogHighlightBytes)
	rows := strings.Split(source, "\n")
	if got := strings.Join(p.outputColors(Block{}, rows), "\n"); got != source {
		t.Fatal("oversized output was decorated")
	}
	block := Block{Kind: "reads", Verb: "Read", Reads: []Read{{Path: "file.py"}}}
	if got := p.outputColors(block, []string{"return 42"})[0]; got == "return 42" || ansi.Strip(got) != "return 42" {
		t.Fatalf("file hint lost: %q", got)
	}
}

func TestDiffOutputUsesPaneSyntaxAndRetainedTailContext(t *testing.T) {
	source := "amber1\n--- a/sample.go\n+++ b/sample.go\n@@ -1,3 +1,3 @@\n package main\n \n-var answer = 41\n+var answer = 42"
	for _, theme := range []livediff.Theme{livediff.DarkTheme, livediff.LightTheme} {
		p := Painter{Theme: theme}
		var renderer livediff.Renderer
		want, err := renderer.ColorSource(t.Context(), theme, "sample.go", "package main\n\nvar answer = 42\n")
		if err != nil {
			t.Fatal(err)
		}
		for _, verb := range []string{"Run", "Diff"} {
			var output Output
			output.Write(source)
			block := Block{Kind: "op", Verb: verb, Code: "mchanges amber1", Output: &output, Tail: []string{"-var answer = 41", "+var answer = 42"}, TailOmitted: 6}
			tail := p.tailColors(block)
			if tail[1] != livediff.SourceLine(theme, ansi.StringWidth(want[2])+4, "", want[2], '+') || !strings.Contains(tail[1], theme.RowBackground('+')) {
				t.Fatalf("tail lacks pane syntax: %q, want %q", tail, want[2])
			}
			for _, collapsed := range []bool{false, true} {
				block.Collapsed = collapsed
				page := p.DialogPage(block, 120)
				if page.Text != strings.ReplaceAll(source, "\n \n", "\n\n") {
					t.Fatalf("copy changed: %q", page.Text)
				}
				found := false
				for _, line := range page.Lines {
					if line.Text == tail[1] {
						found = true
					}
				}
				if !found {
					t.Fatal("dialog differs from streaming tail")
				}
			}
		}
	}
}

func TestCapturedEditDialogUsesPaneSyntax(t *testing.T) {
	p := Painter{Theme: livediff.DarkTheme}
	source := "--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n-var answer = 41\n+var answer = 42"
	block := Block{Kind: "op", Verb: "Edit", Path: "main.go", Code: source, Lang: "diff", Fenced: true}
	page := p.DialogPage(block, 120)
	var renderer livediff.Renderer
	want, err := renderer.ColorSource(t.Context(), p.Theme, "main.go", "var answer = 42\n")
	if err != nil {
		t.Fatal(err)
	}
	if page.Text != source {
		t.Fatal("edit copy changed")
	}
	found := false
	for _, line := range page.Lines {
		if line.Text == livediff.SourceLine(p.Theme, ansi.StringWidth(want[0])+4, "", want[0], '+') {
			found = true
		}
	}
	if !found {
		t.Fatal("clicked edit lacks pane syntax")
	}
}

func TestTimestampedDiffRetainedSyntax(t *testing.T) {
	var output Output
	output.Write("--- old path.go\t2026-09-30 10:00:00.123 +0800\n+++ new path.go\t2026-09-30 10:01:00.123 +0800\n@@ -1 +1 @@\n-var answer = 41\n+var answer = 42")
	p := Painter{Theme: livediff.DarkTheme}
	block := Block{Kind: "op", Verb: "Run", Output: &output, Tail: []string{"+var answer = 42"}, TailOmitted: 4}
	var renderer livediff.Renderer
	source, err := renderer.ColorSource(t.Context(), p.Theme, "path.go", "var answer = 42\n")
	if err != nil {
		t.Fatal(err)
	}
	want := livediff.SourceLine(p.Theme, ansi.StringWidth(source[0])+4, "", source[0], '+')
	if got := p.tailColors(block)[0]; got != want {
		t.Fatalf("timestamp lost tail syntax: %q", got)
	}
	page := p.DialogPage(block, 120)
	if page.Lines[len(page.Lines)-1].Text != want {
		t.Fatal("timestamp lost dialog syntax")
	}
}
