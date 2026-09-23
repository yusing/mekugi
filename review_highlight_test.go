package mekugi

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestReviewCompositionHighlights(t *testing.T) {
	type step struct {
		text                  string
		highlighted, reviewed bool
	}
	for _, tc := range []struct {
		name, base string
		steps      []step
		want       []bool
	}{
		{"adjacent", "a\nb\n", []step{{"A\nb\n", false, false}, {"A\nB\n", true, false}}, []bool{false, true}},
		{"overlap", "a\nb\n", []step{{"A\nb\n", false, false}, {"AA\nb\n", true, false}}, []bool{true}},
		{"insert shifts old edit", "a\nb\n", []step{{"a\nB\n", false, false}, {"new\na\nB\n", true, false}}, []bool{true, false}},
		{"delete shifts old edit", "a\nb\nc\n", []step{{"a\nb\nC\n", false, false}, {"b\nC\n", true, false}}, []bool{true, false}},
		{"multiple captures", "a\nb\n", []step{{"A\nb\n", true, false}, {"A\nB\n", true, false}}, []bool{true, true}},
		{"full revert", "a\nb\n", []step{{"A\nb\n", false, false}, {"a\nb\n", true, false}}, nil},
		{"repeated line revert", "a\na\n", []step{{"a\nb\n", false, false}, {"a\na\n", true, false}}, nil},
		{"reviewed receipt", "a\nb\n", []step{{"A\nb\n", false, false}, {"A\nB\n", true, true}}, []bool{false}},
		{"line endings", "a\r\nb\r\n", []step{{"A\r\nb\r\n", false, false}, {"A\r\nB", true, false}}, []bool{false, true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var c ReviewComposition
			before := tc.base
			for _, step := range tc.steps {
				if err := c.ApplyWithHighlight(composeCapture(before, step.text), step.reviewed, step.highlighted); err != nil {
					t.Fatal(err)
				}
				before = step.text
			}
			var flags []bool
			for _, file := range c.FilesWithHighlights() {
				flags = append(flags, file.Highlighted)
			}
			if !slices.Equal(flags, tc.want) {
				t.Fatalf("highlights = %v, want %v; diff:\n%s", flags, tc.want, composedText(&c))
			}
		})
	}
}

func TestReviewCompositionHighlightPathOnly(t *testing.T) {
	move := RenderReviewFile("old", "new", "", "")
	var c ReviewComposition
	if err := c.ApplyWithHighlight(move, false, true); err != nil {
		t.Fatal(err)
	}
	files := c.FilesWithHighlights()
	if len(files) != 1 || !files[0].Highlighted || files[0].BeforePath != "old" || files[0].AfterPath != "new" {
		t.Fatalf("path-only highlight = %#v", files)
	}
}

func TestReviewCompositionHighlightFailureIsAtomic(t *testing.T) {
	var c ReviewComposition
	if err := c.ApplyWithHighlight(composeCapture("a\nb\n", "A\nb\n"), false, true); err != nil {
		t.Fatal(err)
	}
	before := c.FilesWithHighlights()
	if err := c.ApplyWithHighlight(composeCapture("wrong\nb\n", "other\nb\n"), false, false); err == nil {
		t.Fatal("inconsistent capture succeeded")
	}
	if !reflect.DeepEqual(before, c.FilesWithHighlights()) {
		t.Fatal("failed capture mutated highlights")
	}
}

func TestReviewCompositionHighlightRevertLeavesUnrelatedHunk(t *testing.T) {
	base := "a\n" + strings.Repeat("context\n", 12) + "b\n"
	first := strings.Replace(base, "a\n", "A\n", 1)
	second := strings.TrimSuffix(first, "b\n") + "B\n"
	reverted := strings.Replace(second, "A\n", "a\n", 1)
	var c ReviewComposition
	for i, pair := range [][2]string{{base, first}, {first, second}, {second, reverted}} {
		if err := c.ApplyWithHighlight(composeCapture(pair[0], pair[1]), false, i == 2); err != nil {
			t.Fatal(err)
		}
	}
	files := c.FilesWithHighlights()
	if len(files) != 1 || files[0].Highlighted || !strings.Contains(files[0].Diff, "+B\n") {
		t.Fatalf("revert highlighted an unrelated surviving hunk: %#v", files)
	}
}

func TestReviewLineCounts(t *testing.T) {
	for _, tc := range []struct {
		diff           string
		added, removed int
	}{
		{"--- old\n+++ new\n", 0, 0},
		{"move \"old\" -> \"new\"\n", 0, 0},
		{"--- old\n+++ new\n@@ -1,2 +1,3 @@\n same\r\n-old\r\n+new\r\n+extra\r\n", 2, 1},
		{"--- old\n+++ new\n@@ -1 +1 @@\n-old\n\\ No newline at end of file\n+new\n\\ No newline at end of file\n", 1, 1},
		{"--- old\n+++ new\n@@ -1 +1 @@\n---source\n+++source\n@@ -20 +20,2 @@\n-old\n+new\n+extra\n", 3, 2},
	} {
		added, removed := (ReviewFile{Diff: tc.diff}).LineCounts()
		if added != tc.added || removed != tc.removed {
			t.Fatalf("counts = +%d -%d, want +%d -%d for %q", added, removed, tc.added, tc.removed, tc.diff)
		}
	}
	file := composeCapture("same\nold\n", "same\nnew\nextra\n")
	if !strings.HasSuffix(ReviewStat([]ReviewFile{file}), " 1 file changed, 2 insertions(+), 1 deletion(-)\n") {
		t.Fatalf("structured counts changed summary output: %q", ReviewStat([]ReviewFile{file}))
	}
}
