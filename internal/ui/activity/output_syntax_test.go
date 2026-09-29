package activity

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
)

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
