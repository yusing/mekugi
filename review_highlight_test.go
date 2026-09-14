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
			var files []ReviewFile
			for _, file := range c.FilesWithHighlights() {
				flags = append(flags, file.Highlighted)
				files = append(files, file.ReviewFile)
			}
			if !slices.Equal(flags, tc.want) {
				t.Fatalf("highlights = %v, want %v; diff:\n%s", flags, tc.want, composedText(&c))
			}
			if !reflect.DeepEqual(c.Files(), files) {
				t.Fatal("highlight metadata changed the ordinary review projection")
			}
			c.Flush()
			if len(c.FilesWithHighlights()) != 0 {
				t.Fatal("flush retained highlighted regions")
			}
		})
	}
}

func TestReviewCompositionHighlightPathOnly(t *testing.T) {
	move := reviewFiles([]change{{kind: changeUpdate, originalPath: "old", path: "new"}})[0]
	var c ReviewComposition
	if err := c.ApplyWithHighlight(move, false, true); err != nil {
		t.Fatal(err)
	}
	files := c.FilesWithHighlights()
	if len(files) != 1 || !files[0].Highlighted || files[0].BeforePath != "old" || files[0].AfterPath != "new" {
		t.Fatalf("path-only highlight = %#v", files)
	}
	c.Flush()
	if len(c.FilesWithHighlights()) != 0 {
		t.Fatal("flush retained highlighted move")
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
