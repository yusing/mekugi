package activity

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

const sectionedDiff = "amber1\n--- /dev/null\n+++ \"internal/a new.go\"\n@@ -0,0 +1,2 @@\n+package main\n+var answer = 42\n--- old.go\n+++ new.go\n@@ -1 +1 @@\n--- source\n+++ source\n--- deleted.py\n+++ /dev/null\n@@ -1 +0,0 @@\n-return 1"

func TestUISnapshotDiffOutputSections(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		theme      livediff.Theme
		width      int
	}{
		{"dark", "wide", livediff.DarkTheme, 80}, {"light", "wide", livediff.LightTheme, 80}, {"narrow", "narrow", livediff.TerminalTheme, 32},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := Painter{Theme: tc.theme}
			block := Block{Kind: "op", Verb: "Diff", Code: "mchanges --net", Tail: strings.Split(sectionedDiff, "\n")}
			rows := p.Block(block, tc.width)
			page := p.DialogPage(block, tc.width-4)
			var body []string
			for i := range page.Lines {
				body = append(body, page.Rows(i, tc.width-4)...)
			}
			rows = append(rows, p.Dialog(DialogFrame{Page: page, Rows: body, Total: len(body), Footer: "esc close"}, tc.width, len(body)+7)...)
			uisnapshot.Assert(t, "testdata/snapshots/diff_output_"+tc.text+".txt", strings.Join(rows, "\n")+"\n")
			uisnapshot.AssertTerminal(t, "testdata/snapshots/diff_output_styles_"+tc.name+".txt", append(rows, "plain after diff"), tc.width)
		})
	}
}

func TestDiffOutputSourceAndLayout(t *testing.T) {
	block := Block{Kind: "op", Verb: "Diff", Tail: strings.Split(sectionedDiff, "\n")}
	p := Painter{Theme: livediff.DarkTheme, CopySource: true}
	layout := Painter{Theme: p.Theme, LayoutOnly: true}
	for _, width := range []int{18, 80} {
		if got, want := ansi.Strip(strings.Join(p.Block(block, width), "\n")), ansi.Strip(strings.Join(layout.Block(block, width), "\n")); got != want {
			t.Fatalf("layout changed: %q != %q", got, want)
		}
		page := p.DialogPage(block, width)
		if page.Text != sectionedDiff || strings.Join(block.Tail, "\n") != sectionedDiff {
			t.Fatal("raw output changed")
		}
		for i, line := range page.Lines {
			if line.copySource == nil {
				continue
			}
			var parts []CopyFragment
			for _, row := range page.Rows(i, width) {
				_, spans := ExtractCopy(row)
				for _, span := range spans {
					if part, ok := span.Clip(0, width); ok {
						parts = append(parts, part)
					}
				}
			}
			if got := CopyText(parts); got != block.Tail[i] {
				t.Fatalf("header copy %d = %q, want %q", i, got, block.Tail[i])
			}
		}
	}
	// Only the three actual header pairs change, not header-like hunk source.
	got := p.outputColors(block, block.Tail)
	for i, row := range got {
		if slices.Contains([]int{1, 2, 6, 7, 11, 12}, i) {
			continue
		}
		if ansi.Strip(row) != block.Tail[i] {
			t.Fatalf("source row %d changed", i)
		}
	}
	if !strings.Contains(ansi.Strip(got[2]), "A internal/a new.go +2 -0") || !strings.Contains(ansi.Strip(got[7]), "R old.go => new.go +1 -1") || !strings.Contains(ansi.Strip(got[12]), "D deleted.py +0 -1") {
		t.Fatalf("headers: %q", got)
	}
}

func TestDiffOutputPartialCounts(t *testing.T) {
	for _, tc := range []struct {
		name, source string
		running      bool
		want         string
	}{
		{"partial hunk", "--- a.go\n+++ a.go\n@@ -1 +1,2 @@\n-old\n+new", false, "M a.go counts unavailable"},
		{"stream", "--- a.go\n+++ a.go\n@@ -1 +1 @@\n-old\n+new", true, "M a.go counts unavailable"},
		{"metadata only", "--- a.go\n+++ b.go", false, "R a.go => b.go counts unavailable"},
		{"clipped header", "+++ a.go\n@@ -1 +1 @@\n-old\n+new", false, "+++ a.go"},
		{"unrecognized", "diagnostic output", false, "diagnostic output"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := Painter{}
			block := Block{Kind: "op", Verb: "Diff", Label: "working tree · git", Tail: strings.Split(tc.source, "\n"), Running: tc.running}
			got := ansi.Strip(strings.Join(p.outputColors(block, block.Tail), "\n"))
			if !strings.Contains(got, tc.want) {
				t.Fatalf("%s: %q", tc.name, got)
			}
		})
	}
	// A selected preview ending before the retained hunk does not claim totals.
	rows := strings.Split("--- a.go\n+++ a.go\n@@ -1 +1,2 @@\n-old\n+new\n+last", "\n")
	block := selectedOutputBlock(Block{Kind: "op", Verb: "Diff", Label: "working tree · git"}, rows, 0)
	block.Output.Finish(nil, new(0))
	block.Tail = block.Tail[:4]
	p := Painter{}
	if got := fmt.Sprint(p.tailColors(block)); !strings.Contains(got, "counts unavailable") {
		t.Fatal(got)
	}
}
