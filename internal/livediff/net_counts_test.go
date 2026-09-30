package livediff

import (
	"fmt"
	"testing"

	"github.com/yusing/mekugi"
)

func netCountsCapture(order uint64, caller, beforePath, afterPath, before, after string) Chunk {
	return Chunk{
		Key: fmt.Sprintf("capture-%d", order), CaptureOrder: order,
		Origin: Origin{Change: fmt.Sprintf("change-%d", order), Caller: caller},
		Review: mekugi.RenderReviewFile(beforePath, afterPath, before, after),
	}
}

func TestFileNetCountsComposesVisibleHistory(t *testing.T) {
	tests := []struct {
		name     string
		captures []Chunk
		want     Counts
	}{
		{"superseded writes", []Chunk{
			netCountsCapture(1, "/root", "a", "a", "old\n", "draft\none\n"),
			netCountsCapture(2, "/root", "a", "a", "draft\none\n", "final\n"),
		}, Counts{1, 1}},
		{"delete and recreate", []Chunk{
			netCountsCapture(1, "/root", "a", "", "old\nkeep\n", ""),
			netCountsCapture(2, "/root/worker", "", "a", "", "new\nkeep\n"),
		}, Counts{1, 1}},
		{"rename chain across agents in capture order", []Chunk{
			netCountsCapture(3, "/root", "b", "c", "draft\n", "final\n"),
			netCountsCapture(1, "/root/worker", "a", "b", "old\n", "draft\n"),
			netCountsCapture(2, "/root/other", "b", "b", "draft\n", "draft\n"),
		}, Counts{1, 1}},
		{"created then deleted", []Chunk{
			netCountsCapture(1, "/root", "", "a", "", "temporary\n"),
			netCountsCapture(2, "/root/worker", "a", "", "temporary\n", ""),
		}, Counts{}},
		{"reverted rewrite", []Chunk{
			netCountsCapture(1, "/root", "a", "a", "old\n", "draft\n"),
			netCountsCapture(2, "/root/worker", "a", "a", "draft\n", "old\n"),
		}, Counts{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			view := View{}
			view.Merge(GroupCaptures(tt.captures))
			view.RefreshVisible()
			if len(view.Files) != 1 {
				t.Fatalf("got %d file groups, want one composed history", len(view.Files))
			}
			if got := view.Visible[view.Files[0].Key()].NetCounts(); got != tt.want {
				t.Fatalf("net counts = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestFileNetCountsUnknownEvidence(t *testing.T) {
	for _, kind := range []string{"binary", "incomplete", "inconsistent", "hidden incomplete"} {
		t.Run(kind, func(t *testing.T) {
			first := netCountsCapture(1, "/root/worker", "a", "a", "old\n", "draft\n")
			second := netCountsCapture(2, "/root", "a", "a", "draft\n", "final\n")
			view := View{}
			switch kind {
			case "binary":
				first.Review.Binary = true
			case "incomplete", "hidden incomplete":
				first.Review.Incomplete = "before content unavailable"
			case "inconsistent":
				second.Review = mekugi.RenderReviewFile("a", "a", "unobserved\n", "final\n")
			}
			view.Merge(GroupCaptures([]Chunk{first, second}))
			if kind == "hidden incomplete" {
				view.FilterCaller("/root")
			}
			view.RefreshVisible()
			if got := view.Visible[view.Files[0].Key()].NetCounts(); got != (Counts{-1, -1}) {
				t.Fatalf("%s net counts = %+v, want unknown", kind, got)
			}
		})
	}
}
