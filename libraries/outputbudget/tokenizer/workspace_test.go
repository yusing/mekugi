package tokenizer

import (
	"slices"
	"strings"
	"testing"
)

func TestCountAllocationGrowth(t *testing.T) {
	c, err := New()
	if err != nil {
		t.Fatal(err)
	}
	const line = "Review the implementation and preserve exact input. 世界🙂\n"
	perLine, err := c.Count(line)
	if err != nil {
		t.Fatal(err)
	}
	allocations := func(lines int) float64 {
		value := strings.Repeat(line, lines)
		return testing.AllocsPerRun(3, func() {
			count, err := c.Count(value)
			if err != nil || count != lines*perLine {
				t.Fatalf("count = %d, want %d: %v", count, lines*perLine, err)
			}
		})
	}
	// Match storage can grow, but each piece must reuse the merge buffers.
	if small, large := allocations(32), allocations(512); large > small+32 {
		t.Fatalf("allocations grew per piece: %.0f -> %.0f", small, large)
	}
}

func TestMergeWorkspaceGrowingAndShrinking(t *testing.T) {
	c, err := New()
	if err != nil {
		t.Fatal(err)
	}
	var workspace mergeWorkspace
	for _, n := range []int{3, 256, 257, 1024, 4097, 512, 64, 4097, 1} {
		for _, atom := range []string{" \t\r\n", "!!!///", "abc界"} {
			piece := strings.Repeat(atom, n)
			want := c.mergePairsWithWorkspace(piece, &mergeWorkspace{})
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
