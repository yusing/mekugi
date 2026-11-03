package capture

import (
	"context"
	"net/http"
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

func TestOutputDeduplicationRuntimeFamily(t *testing.T) {
	family := new(OutputDeduplicationTotals)
	open := func(totals *OutputDeduplicationTotals) *Recorder {
		t.Helper()
		r, err := new(Service).Open(Config{Deduplication: totals})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = r.Close() })
		return r
	}
	parent, child := open(family), open(family)
	ctx := context.WithValue(t.Context(), captureKey{}, child.beginRequest(http.Header{"Thread-Id": {"child"}}))
	ObserveOutputDeduplication(ctx, 2, -3, true)
	ObserveOutputDeduplication(ctx, 1, 100, false)
	if err := child.Close(); err != nil {
		t.Fatal(err)
	}
	want := OutputDeduplicationMetrics{Projections: 2, Occurrences: 3, SavedTokens: -3, Unmeasured: 1}
	snapshot := parent.Snapshot()
	if snapshot.OutputDeduplicationThreads["child"] != want || snapshot.OutputDeduplication != (OutputDeduplicationMetrics{}) {
		t.Fatalf("settled child's receipts or runtime-local total: %+v", snapshot)
	}
	delete(snapshot.OutputDeduplicationThreads, "child")
	if parent.Snapshot().OutputDeduplicationThreads["child"] != want {
		t.Fatal("family snapshot shares its thread map")
	}
	if fresh := open(nil).Snapshot(); fresh.OutputDeduplicationThreads != nil {
		t.Fatal("a new launch inherited family receipts")
	}
}
