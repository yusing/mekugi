package capture

import (
	"context"
	"testing"
)

func TestOutputDeduplicationMetrics(t *testing.T) {
	r, err := new(Service).Open(Config{Mode: "mekugi"})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ctx := context.WithValue(t.Context(), captureKey{}, &requestState{recorder: r})
	ObserveOutputDeduplication(ctx, 2, -3, true)
	first := r.Snapshot()
	ObserveOutputDeduplication(ctx, 2, 7, false)
	second := r.Snapshot().OutputDeduplication
	if first.OutputDeduplication.Occurrences != 2 || second.Occurrences != 4 || second.Projections != 2 || second.SavedTokens != -3 || second.Unmeasured != 1 {
		t.Fatalf("metrics: %+v / %+v", first.OutputDeduplication, second)
	}
}
