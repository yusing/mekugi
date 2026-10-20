package router

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestLiveActivitySeparateRosterSharesSelectionAndFeedState(t *testing.T) {
	view := liveActivityTestView("/root/alpha", "/root/beta")
	view.feedOnly = true
	now := time.Now()
	feed := view.render(90, 14, now)
	if !strings.Contains(strings.Join(plainLines(feed), "\n"), "Read") {
		t.Fatalf("feed-only view lost its activity feed: %q", plainLines(feed))
	}
	if len(view.hits) != 0 {
		t.Fatalf("feed view retained roster pointer hits: %+v", view.hits)
	}
	feedTop, feedLeft, feedRight := view.feedTop, view.feedLeft, view.feedRight
	feedLines, feedRows := view.feedLines, view.feedRows
	feedSnippets := slices.Clone(view.feedSnippets)

	roster := view.renderRosterPane(55, 10, now)
	if len(roster) != 10 {
		t.Fatalf("roster pane rows = %d, want 10", len(roster))
	}
	if plain := strings.Join(plainLines(roster), "\n"); !strings.Contains(plain, "alpha") || !strings.Contains(plain, "beta") {
		t.Fatalf("separate roster omitted agents: %s", plain)
	}
	if view.feedTop != feedTop || view.feedLeft != feedLeft || view.feedRight != feedRight ||
		view.feedLines != feedLines || view.feedRows != feedRows || !slices.Equal(view.feedSnippets, feedSnippets) {
		t.Fatalf("roster rendering overwrote feed hit-test/scroll metadata")
	}
	var betaHit liveActivityHit
	for _, hit := range view.hits {
		if hit.agent == "/root/beta" {
			betaHit = hit
			break
		}
	}
	if betaHit.agent == "" {
		t.Fatalf("separate roster did not publish its own agent hit regions: %+v", view.hits)
	}
}

// Exercise the shared roster renderer in isolation, without a native terminal shell.
// renderRosterPane shares selection with the feed without duplicating its state.
func (v *liveActivityView) renderRosterPane(width, height int, now time.Time) []string {
	width, height = max(1, width-1), max(1, height)
	v.hits = v.hits[:0]
	rows := v.roster()
	v.rosterTop, v.rosterBottom, v.rosterRight = 2, height, width
	lines := []string{v.renderHeader(rows, width, true)}
	if height > 1 && len(rows) > 0 {
		lines = append(lines, v.renderRoster(rows, width, height-1, now)...)
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return lines[:height]
}
