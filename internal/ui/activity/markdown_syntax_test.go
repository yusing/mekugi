package activity

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func TestMarkdownInlineSyntax(t *testing.T) {
	for _, theme := range []livediff.Theme{livediff.DarkTheme, livediff.LightTheme, 0} {
		p := Painter{Theme: theme}
		for _, tc := range []struct{ code, lang string }{
			{"package main", "go"},
			{"func _ready(): print(42)", "gd"},
			{"#!/bin/bash", "bash"},
			{"notify:         notif.FromCtx(parent.Context()).Notify,", "text"},
			{"FOO=bar command args...", "text"},
			{"\"not := code\"", "text"},
			{"internal/ui/activity/paint.go", "text"},
			{"猫👩‍💻", "text"},
			{"completed successfully", "text"},
		} {
			code := tc.code
			t.Run(fmt.Sprint(theme)+"/"+code, func(t *testing.T) {
				source := "**before ``" + code + "`` after** tail"
				got := p.Inline(source)
				if ansi.Strip(got) != "before "+code+" after tail" {
					t.Fatalf("changed inline text: %q", got)
				}
				screen := vt.NewEmulator(ansi.StringWidth(got)+1, 1)
				defer screen.Close()
				fmt.Fprint(screen, got)
				expected := vt.NewEmulator(ansi.StringWidth(code)+1, 1)
				defer expected.Close()
				highlighted := code
				if tc.lang != "text" {
					highlighted = strings.Join(p.Highlight(tc.lang, code), "\n")
				}
				fmt.Fprint(expected, p.Theme.Accent()+strings.ReplaceAll(highlighted, "\x1b[39m", p.Theme.Accent())+p.Theme.Accent()+" ")
				distinct := false
				for x := range ansi.StringWidth(code) {
					cell, want := screen.CellAt(7+x, 0), expected.CellAt(x, 0)
					if cell.Content != want.Content || cell.Style.Fg != want.Style.Fg || (cell.Content != "" && cell.Style.Attrs&uv.AttrBold == 0) {
						t.Fatalf("inline cell %d = %+v, expected syntax %+v with bold", x, cell, want)
					}
					distinct = distinct || (cell.Content != "" && cell.Style.Fg != expected.CellAt(ansi.StringWidth(code), 0).Style.Fg)
				}
				if (tc.lang != "text") != distinct && theme != 0 {
					t.Fatalf("%s snippet has unexpected syntax colors", tc.lang)
				}
				if cell := screen.CellAt(7+ansi.StringWidth(code)+1, 0); cell.Style.Fg != nil || cell.Style.Attrs&uv.AttrBold == 0 {
					t.Fatalf("syntax leaked into bold prose: %+v", cell.Style)
				}
				if cell := screen.CellAt(ansi.StringWidth(got)-1, 0); !cell.Style.IsZero() {
					t.Fatalf("style leaked after prose: %+v", cell.Style)
				}
			})
		}
	}
}

const markdownSyntaxSource = "Run `git commit -m 'amend: parser'` then `echo \"$HOME\"`.\n\n```go\nfunc main() { println(42) }\n```\n\n```bash\nGIT_SEQUENCE_EDITOR=: git rebase -i --autosquash HEAD~2\n```"

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

func TestMarkdownFencedSyntaxPreserved(t *testing.T) {
	for _, theme := range []livediff.Theme{livediff.DarkTheme, livediff.LightTheme} {
		p := Painter{Theme: theme}
		for lang, source := range map[string]string{"go": "func main() { println(42) }", "bash": "echo \"$HOME\"", "diff": "+added\n-removed"} {
			rows := strings.Join(p.Markdown("```"+lang+"\n"+source+"\n```", 80), "\n")
			for _, highlighted := range p.Highlight(lang, source) {
				if highlighted == ansi.Strip(highlighted) || !strings.Contains(rows, highlighted) {
					t.Fatalf("%s fence lost highlighting: %q", lang, rows)
				}
			}
		}
	}
}
