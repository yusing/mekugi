package launch

import (
	"slices"
	"strings"
	"testing"
	"time"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

func TestLiveActivitySeparateRosterSharesSelectionAndFeedState(t *testing.T) {
	view := liveActivityTestView("/root/alpha", "/root/beta")
	view.FeedOnly = true
	now := time.Now()
	feed := view.Render(90, 14, now)
	if !strings.Contains(strings.Join(plainLines(feed), "\n"), "Read") {
		t.Fatalf("feed-only view lost its activity feed: %q", plainLines(feed))
	}
	if len(view.Hits) != 0 {
		t.Fatalf("feed view retained roster pointer hits: %+v", view.Hits)
	}
	feedTop, feedLeft, feedRight := view.FeedTop, view.FeedLeft, view.FeedRight
	feedLines, feedRows := view.FeedLines, view.FeedRows
	feedSnippets := slices.Clone(view.FeedSnippets)

	roster := view.renderRosterPane(55, 10, now)
	if len(roster) != 10 {
		t.Fatalf("roster pane rows = %d, want 10", len(roster))
	}
	if plain := strings.Join(plainLines(roster), "\n"); !strings.Contains(plain, "alpha") || !strings.Contains(plain, "beta") {
		t.Fatalf("separate roster omitted agents: %s", plain)
	}
	if view.FeedTop != feedTop || view.FeedLeft != feedLeft || view.FeedRight != feedRight ||
		view.FeedLines != feedLines || view.FeedRows != feedRows || !slices.Equal(view.FeedSnippets, feedSnippets) {
		t.Fatalf("roster rendering overwrote feed hit-test/scroll metadata")
	}
	var betaHit activityui.Hit
	for _, hit := range view.Hits {
		if hit.Agent == "/root/beta" {
			betaHit = hit
			break
		}
	}
	if betaHit.Agent == "" {
		t.Fatalf("separate roster did not publish its own agent hit regions: %+v", view.Hits)
	}
}

// Exercise the shared roster renderer in isolation, without a native terminal shell.
// renderRosterPane shares selection with the feed without duplicating its state.
func (v *activityAdapter) renderRosterPane(width, height int, now time.Time) []string {
	width, height = max(1, width-1), max(1, height)
	v.Hits = v.Hits[:0]
	rows := v.testRoster()
	v.RosterTop, v.RosterBottom, v.RosterRight = 2, height, width
	lines := []string{v.RenderHeader(rows, width, true)}
	if height > 1 && len(rows) > 0 {
		lines = append(lines, v.RenderRoster(rows, width, height-1, now)...)
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return lines[:height]
}
