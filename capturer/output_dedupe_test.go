package capturer

import (
	"context"
	"testing"
)

func TestOutputDeduplicationMetrics(t *testing.T) {
	r, err := New(Config{Mode: "mekugi"})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ctx := context.WithValue(t.Context(), captureKey{}, &requestState{recorder: r})
	before, after := []byte(`["one two three four five six seven eight nine ten"]`), []byte(`["repeated"]`)
	a, _ := contentTokens(before, r.codec)
	b, _ := contentTokens(after, r.codec)
	ObserveOutputDeduplication(ctx, before, after, 2)
	first := r.Snapshot()
	ObserveOutputDeduplication(ctx, before, after, 2)
	second := r.Snapshot().OutputDeduplication
	if first.OutputDeduplication.Occurrences != 2 || second.Occurrences != 4 || second.Projections != 2 || second.SavedTokens != int64(2*(a-b)) {
		t.Fatalf("metrics: %+v / %+v", first.OutputDeduplication, second)
	}
}
