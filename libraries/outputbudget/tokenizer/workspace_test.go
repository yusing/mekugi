package tokenizer

import (
	"slices"
	"strings"
	"testing"
)

func TestMergeWorkspaceGrowingAndShrinking(t *testing.T) {
	c, err := New()
	if err != nil {
		t.Fatal(err)
	}
	var workspace mergeWorkspace
	for _, n := range []int{3, 256, 257, 1024, 4097, 512, 64, 4097, 1} {
		for _, atom := range []string{" \t\r\n", "!!!///", "abc界"} {
			piece := strings.Repeat(atom, n)
			want := c.mergePairs(piece)
			got := c.mergeLongPieceWithWorkspace(piece, &workspace)
			if !slices.EqualFunc(got, want, func(a, b part) bool { return a.offset == b.offset }) {
				t.Fatalf("reused merge differs: atom=%q n=%d", atom, n)
			}
			value := piece + "\n/\nhello\n"
			count, err := c.countWithWorkspace(value, &workspace)
			full, fullErr := c.Count(value)
			if err != nil || fullErr != nil || count != full {
				t.Fatalf("reused lexer differs: %d != %d: %v %v", count, full, err, fullErr)
			}
		}
	}
}
