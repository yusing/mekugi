package activity

import (
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func TestUISnapshotMarkdownStyles(t *testing.T) {
	for _, tc := range []struct {
		name  string
		theme livediff.Theme
	}{
		{"terminal", livediff.TerminalTheme},
		{"dark", livediff.DarkTheme},
		{"light", livediff.LightTheme},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := Painter{Theme: tc.theme, FileLink: func(string) bool { return true }}
			var rows []string
			for _, code := range []string{
				"package main",
				"func _ready(): print(42)",
				"#!/bin/bash",
				"notify:         notif.FromCtx(parent.Context()).Notify,",
				"FOO=bar command args...",
				"\"not := code\"",
				"internal/ui/activity/paint.go",
				"猫👩‍💻",
				"completed successfully",
			} {
				rows = append(rows, p.Inline("**before ``"+code+"`` after** tail"))
			}
			// Wrapped inline commands and Go, shell, and diff fences retain syntax.
			source := markdownSyntaxSource + "\n\n```bash\necho \"$HOME\"\n```\n\n```diff\n+added\n-removed\n```"
			rows = append(rows, p.Markdown(source, 32)...)
			rows = append(rows, "plain after syntax")
			uisnapshot.AssertTerminal(t, "testdata/snapshots/markdown_styles_"+tc.name+".txt", rows, 80)
		})
	}
}

const markdownSyntaxSource = "Run `git commit -m 'amend: parser'` then `echo \"$HOME\"`.\n\n```go\nfunc main() { println(42) }\n```\n\n```bash\nGIT_SEQUENCE_EDITOR=: git rebase -i --autosquash HEAD~2\n```"

func TestUISnapshotMarkdownHeadings(t *testing.T) {
	for _, tc := range []struct {
		name  string
		theme livediff.Theme
	}{{"terminal", livediff.TerminalTheme}, {"dark", livediff.DarkTheme}, {"light", livediff.LightTheme}} {
		for _, width := range []int{12, 40} {
			t.Run(tc.name+"_"+strconv.Itoa(width), func(t *testing.T) {
				p := Painter{Theme: tc.theme, CopySource: true}
				rows := p.Markdown("# Main heading\n\n## Section heading ##\n\n### Subsection with `code` and **bold** text\n\n#### Detail\n\n##### Deeper\n\n###### Sixth\n\n# H\n\nSetext title\n===\n\nSetext section\n---\n\n#nospace and ####### seven stay text.\n\nPlain after headings.", width)
				for i := range rows {
					rows[i], _ = ExtractCopy(rows[i])
				}
				uisnapshot.AssertTerminal(t, "testdata/snapshots/markdown_headings_"+tc.name+"_"+strconv.Itoa(width)+".txt", rows, 40)
			})
		}
	}
}

func TestUISnapshotMarkdownSyntax(t *testing.T) {
	for _, width := range []int{18, 72} {
		t.Run(strconv.Itoa(width), func(t *testing.T) {
			p := Painter{Theme: livediff.DarkTheme, CopySource: true}
			rows := p.Markdown(markdownSyntaxSource, width)
			uisnapshot.Assert(t, "testdata/snapshots/markdown_syntax_"+strconv.Itoa(width)+".txt", strings.Join(rows, "\n")+"\n")
			layout := Painter{Theme: livediff.DarkTheme, LayoutOnly: true}
			plain := layout.Markdown(markdownSyntaxSource, width)
			if len(rows) != len(plain) {
				t.Fatal("highlighting changed row count")
			}
			for i, row := range rows {
				if ansi.Strip(row) != ansi.Strip(plain[i]) || ansi.StringWidth(row) > width {
					t.Fatalf("row %d changed geometry", i)
				}
			}
		})
	}
}

func TestMarkdownSyntaxSourceCopy(t *testing.T) {
	want := "Run `git commit -m 'amend: parser'` then `echo \"$HOME\"`.\n\nfunc main() { println(42) }\n\nGIT_SEQUENCE_EDITOR=: git rebase -i --autosquash HEAD~2"
	for _, width := range []int{8, 18, 72} {
		if got := selectedMarkdown(t, markdownSyntaxSource, width); got != want {
			t.Fatalf("width %d: copy = %q, want %q", width, got, want)
		}
	}
}
