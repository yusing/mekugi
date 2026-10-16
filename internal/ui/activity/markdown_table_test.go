package activity_test

import (
	"testing"

	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

func TestMarkdownTableSummary(t *testing.T) {
	p := activityui.Painter{}
	for _, tc := range []struct{ source, want string }{
		{"A | B\n--- | ---\nx | y", "A: x · B: y"},
		{"> A | B\n> --- | ---\n> x | y", "A: x · B: y"},
		{"> > A | B\n> > --- | ---\n> > x | y", "A: x · B: y"},
		{"\n**A** | B\n--- | ---", "A · B"},
	} {
		got := p.Summary([]activityui.Block{{Kind: "final", Body: tc.source}}, 80)
		if got != tc.want {
			t.Fatalf("summary %q, want %q", got, tc.want)
		}
	}
}
