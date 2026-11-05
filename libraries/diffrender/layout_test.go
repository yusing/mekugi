package diffrender

import "testing"

func TestViewportHunkWorkIsBoundedByIntersections(t *testing.T) {
	layout := Layout{syntaxHunks: make([]syntaxHunk, 4096)}
	for i := range layout.syntaxHunks {
		layout.syntaxHunks[i] = syntaxHunk{start: 10 * i, end: 10*i + 5, painted: true}
	}
	for _, test := range []struct {
		start, end, first, count int
	}{
		{0, 1, 0, 1}, {5, 10, 0, 0}, {10, 10, 0, 0},
		{40942, 40951, 4094, 2}, {40945, 40950, 0, 0},
		{40950, 40955, 4095, 1}, {40955, 50000, 0, 0},
	} {
		hunks := layout.viewportHunks(test.start, test.end)
		if len(hunks) != test.count || len(hunks) > 0 && &hunks[0] != &layout.syntaxHunks[test.first] {
			t.Fatalf("viewport [%d,%d) visits %d hunks, want %d at %d", test.start, test.end, len(hunks), test.count, test.first)
		}
		// Use the same bounded walk through the consuming decoration path.
		// Warm hunks need no renderer or syntax work.
		if err := layout.PaintViewport(t.Context(), test.start, test.end); err != nil {
			t.Fatal(err)
		}
	}
}
