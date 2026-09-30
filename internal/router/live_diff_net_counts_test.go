package router

import (
	"fmt"
	"strings"
	"testing"

	"github.com/yusing/mekugi"
	"github.com/yusing/mekugi/internal/livediff"
)

func TestLiveDiffNetCountsLargeSupersededRewrite(t *testing.T) {
	lines := func(prefix string, count int) string {
		var content strings.Builder
		for i := range count {
			fmt.Fprintf(&content, "%s_%d\n", prefix, i)
		}
		return content.String()
	}
	before, intermediate, final := lines("old", 110), lines("draft", 460), lines("final", 818)
	view := livediff.View{}
	view.Merge(livediff.GroupCaptures([]livediff.Chunk{
		liveDiffNetCapture(1, "/root", "/w/a", "/w/a", before, intermediate),
		liveDiffNetCapture(2, "/root/worker", "/w/a", "/w/a", intermediate, final),
	}))
	got := liveDiffNetCounts(&view)
	if got == nil || *got != (livediff.Counts{Added: 818, Removed: 110}) {
		t.Fatalf("final outcome = %+v, want +818 -110", got)
	}
	var activity livediff.Counts
	for _, count := range liveDiffCallerCounts(view.Files) {
		activity.Added += count.Added
		activity.Removed += count.Removed
	}
	if activity != (livediff.Counts{Added: 1278, Removed: 570}) {
		t.Fatalf("cumulative activity = %+v, want +1278 -570", activity)
	}
}

func liveDiffNetCapture(order uint64, caller, beforePath, afterPath, before, after string) livediff.Chunk {
	return livediff.Chunk{
		Key: fmt.Sprintf("net-%d", order), CaptureOrder: order, Workspace: "/w",
		Origin: livediff.Origin{Change: fmt.Sprintf("change-%d", order), Caller: caller},
		Review: mekugi.RenderReviewFile(beforePath, afterPath, before, after),
	}
}

func TestLiveDiffNetCountsComposesCrossAgentOutcomeNotActivity(t *testing.T) {
	// Receipts arrive out of execution order and one agent supersedes another's
	// rewrite. A rename must retain the original baseline across both agents.
	captures := []livediff.Chunk{
		liveDiffNetCapture(3, "/root", "/w/b", "/w/c", "draft\nextra\n", "final\n"),
		liveDiffNetCapture(1, "/root/worker", "/w/a", "/w/a", "old\n", "draft\nextra\n"),
		liveDiffNetCapture(2, "/root/worker", "/w/a", "/w/b", "draft\nextra\n", "draft\nextra\n"),
		liveDiffNetCapture(4, "/root", "", "/tmp/scratch", "", "scratch\n"),
	}
	view := livediff.View{}
	view.Merge(livediff.GroupCaptures(captures))
	view.RefreshVisible()
	view.FilterCaller("/root")
	got := liveDiffNetCounts(&view)
	if got == nil || *got != (livediff.Counts{Added: 1, Removed: 1}) {
		t.Fatalf("overall net counts = %+v, want +1 -1", got)
	}
	if view.Caller != "/root" {
		t.Fatalf("computing overall counts changed caller filter to %q", view.Caller)
	}
	activity := liveDiffCallerCounts(view.Files)
	if activity["/root"] != (livediff.Counts{Added: 1, Removed: 2}) || activity["/root/worker"] != (livediff.Counts{Added: 2, Removed: 1}) {
		t.Fatalf("activity counts = %+v, want cumulative per-agent edits without scratch", activity)
	}
}

func TestLiveDiffNetCountsDistinguishesCancellationFromNoProjectEvidence(t *testing.T) {
	tests := []struct {
		name     string
		captures []livediff.Chunk
		wantNil  bool
	}{
		{"no captures", nil, true},
		{"scratch only", []livediff.Chunk{
			liveDiffNetCapture(1, "/root", "", "/tmp/scratch", "", "draft\n"),
		}, true},
		{"project edit cancelled", []livediff.Chunk{
			liveDiffNetCapture(1, "/root", "", "/w/a", "", "draft\n"),
			liveDiffNetCapture(2, "/root/worker", "/w/a", "", "draft\n", ""),
		}, false},
		{"delete and recreate original", []livediff.Chunk{
			liveDiffNetCapture(1, "/root", "/w/a", "", "old\n", ""),
			liveDiffNetCapture(2, "/root/worker", "", "/w/a", "", "old\n"),
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			view := livediff.View{}
			view.Merge(livediff.GroupCaptures(tt.captures))
			got := liveDiffNetCounts(&view)
			if (got == nil) != tt.wantNil || got != nil && *got != (livediff.Counts{}) {
				t.Fatalf("net counts = %+v, want nil=%t or known zero", got, tt.wantNil)
			}
		})
	}
}

func TestLiveDiffNetCountsRejectsUnknownProjectEvidence(t *testing.T) {
	for _, kind := range []string{"binary", "incomplete", "inconsistent", "hidden incomplete"} {
		t.Run(kind, func(t *testing.T) {
			first := liveDiffNetCapture(1, "/root/worker", "/w/a", "/w/a", "old\n", "draft\n")
			second := liveDiffNetCapture(2, "/root", "/w/a", "/w/a", "draft\n", "final\n")
			switch kind {
			case "binary":
				first.Review.Binary = true
			case "incomplete", "hidden incomplete":
				first.Review.Incomplete = "before content unavailable"
			case "inconsistent":
				second.Review = mekugi.RenderReviewFile("/w/a", "/w/a", "unobserved\n", "final\n")
			}
			view := livediff.View{}
			view.Merge(livediff.GroupCaptures([]livediff.Chunk{first, second}))
			if kind == "hidden incomplete" {
				view.FilterCaller("/root")
			}
			got := liveDiffNetCounts(&view)
			if got == nil || *got != (livediff.Counts{Added: -1, Removed: -1}) {
				t.Fatalf("%s net counts = %+v, want unknown", kind, got)
			}
		})
	}
}
