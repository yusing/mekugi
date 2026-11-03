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
	if first.OutputDeduplicationThreads != nil {
		t.Fatalf("unattributed receipts gained a thread: %+v", first.OutputDeduplicationThreads)
	}
}

func TestOutputDeduplicationThreadAttribution(t *testing.T) {
	r, err := new(Service).Open(Config{Mode: "mekugi"})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	observe := func(thread string, occurrences uint64, saved int64) {
		ObserveOutputDeduplication(context.WithValue(t.Context(), captureKey{}, &requestState{recorder: r, threadID: thread}), occurrences, saved, true)
	}
	observe("parent", 1, 10)
	observe("child", 2, 20)
	observe("child", 3, 30)
	snapshot := r.Snapshot()
	threads := snapshot.OutputDeduplicationThreads
	if threads["parent"] != (OutputDeduplicationMetrics{Projections: 1, Occurrences: 1, SavedTokens: 10}) || threads["child"] != (OutputDeduplicationMetrics{Projections: 2, Occurrences: 5, SavedTokens: 50}) || snapshot.OutputDeduplication.Occurrences != 6 {
		t.Fatalf("thread attribution: %+v / %+v", threads, snapshot.OutputDeduplication)
	}
	threads["child"] = OutputDeduplicationMetrics{}
	if r.Snapshot().OutputDeduplicationThreads["child"].Projections != 2 {
		t.Fatal("snapshot shares the recorder's thread map")
	}
}
