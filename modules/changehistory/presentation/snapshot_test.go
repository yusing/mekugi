package presentation

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/yusing/mekugi/contracts/activitydetail"
	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/libraries/pathdisplay"
)

func snapshotAttempt(call, thread, item string, chunks ...Chunk) Attempt {
	return Attempt{
		AttemptIdentity: AttemptIdentity{Key: AttemptKey{Workspace: "/w", Call: call}, Change: call, Correlation: item, Stream: 1},
		Operation:       activitydetail.Operation{Thread: thread, Item: item}, Chunks: chunks,
	}
}

func snapshotFixture(t *testing.T, attempts ...Attempt) *Snapshot {
	t.Helper()
	builder := NewBuilder(nil)
	for _, attempt := range attempts {
		if err := builder.Add(attempt); err != nil {
			t.Fatal(err)
		}
	}
	return builder.Finish()
}

func TestSnapshotBuilderPreservesAcceptedEvidence(t *testing.T) {
	chunk := liveDiffNetCapture(1, "/root", "/w/a", "/w/a", "old\n", "draft\nextra\n")
	first := snapshotAttempt("first", "thread", "first", chunk)
	first.Receipt = &Receipt{Thread: "thread", Calls: []string{"first", "sibling"}, Text: "captured", Pages: []activitydetail.Page{{ID: "receipt", Code: "retained"}}}
	builder := NewBuilder(nil)
	if err := builder.Add(first); err != nil {
		t.Fatal(err)
	}
	first.Chunks[0].Review.Diff = "mutated input"
	first.Receipt.Calls[0], first.Receipt.Pages[0].Code, first.Receipt.Text = "mutated", "mutated", "mutated"
	accepted := builder.Finish()
	if got := slices.Collect(accepted.Attempts()); len(got) != 1 || got[0].Chunks[0] != chunk {
		t.Fatalf("accepted attempt changed with input: %+v", got)
	}
	wantReceipt := &Receipt{Thread: "thread", Calls: []string{"first", "sibling"}, Text: "captured", Pages: []activitydetail.Page{{ID: "receipt", Code: "retained"}}}
	if got := accepted.Receipts(); !reflect.DeepEqual(got, []*Receipt{wantReceipt}) {
		t.Fatalf("accepted receipt changed with input: %+v", got)
	}
	request := activitydetail.Request{Operation: first.Operation, Path: "a"}
	frozen := accepted.ActivityDetail(request)
	if len(frozen.Pages) != 1 || !strings.Contains(frozen.Pages[0].Code, "+draft\n+extra") {
		t.Fatalf("original capture unavailable: %+v", frozen)
	}

	if allocations := testing.AllocsPerRun(5, func() {
		unchanged := NewBuilder(accepted)
		if err := unchanged.Add(first); err != nil || unchanged.Finish() != accepted {
			t.Fatal("duplicate attempt changed accepted evidence", err)
		}
	}); allocations > 1 {
		t.Fatalf("unchanged evidence copied collections: %.0f allocations", allocations)
	}

	extension := NewBuilder(accepted)
	if found, err := extension.Known(first.AttemptIdentity); !found || err != nil {
		t.Fatalf("accepted identity: found=%v err=%v", found, err)
	}
	changed := first.AttemptIdentity
	changed.Correlation = "different"
	if _, err := extension.Known(changed); err == nil {
		t.Fatal("changed identity accepted")
	}
	invalid := snapshotAttempt("invalid", "thread", "invalid")
	invalid.Key.Workspace = "relative"
	if err := extension.Add(invalid); err == nil {
		t.Fatal("relative workspace accepted")
	}
	second := snapshotAttempt("second", "thread", "second", liveDiffNetCapture(2, "/root/worker", "/w/a", "/w/a", "draft\nextra\n", "final\n"))
	if err := extension.Add(second); err != nil {
		t.Fatal(err)
	}
	if err := extension.RequirePresent(map[AttemptKey]bool{first.Key: true}); err == nil {
		t.Fatal("removed evidence accepted")
	}
	if err := extension.RequirePresent(map[AttemptKey]bool{first.Key: true, second.Key: true}); err != nil {
		t.Fatal(err)
	}
	next := extension.Finish()
	if accepted.Len() != 1 || next.Len() != 2 || !reflect.DeepEqual(accepted.ActivityDetail(request), frozen) || !reflect.DeepEqual(next.ActivityDetail(request), frozen) {
		t.Fatal("extension changed accepted membership or original invocation detail")
	}
	for _, check := range []struct {
		snapshot *Snapshot
		code     string
		counts   diffmodel.Counts
	}{{accepted, "-old\n+draft\n+extra", diffmodel.Counts{Added: 2, Removed: 1}}, {next, "-old\n+final", diffmodel.Counts{Added: 1, Removed: 1}}} {
		publication := check.snapshot.Publication
		file := publication.Unfiltered[publication.Files[0].Key()]
		if len(file.Chunks) != 1 || !strings.Contains(file.Chunks[0].Review.Diff, check.code) || publication.Counts.Net == nil || *publication.Counts.Net != check.counts {
			t.Fatalf("publication changed: file=%+v counts=%+v", file, publication.Counts.Net)
		}
	}
	if err := extension.Add(snapshotAttempt("third", "thread", "third")); err == nil {
		t.Fatal("published builder accepted another attempt")
	}
}

func TestSnapshotActivityDetailIdentityAndReceiptCalls(t *testing.T) {
	chunk := liveDiffNetCapture(1, "/root", "/w/a", "/w/a", "old\n", "new\n")
	first := snapshotAttempt("first", "main", "shared", chunk)
	childChunk := chunk
	childChunk.Key = "child"
	child := snapshotAttempt("child", "child", "shared", childChunk)
	receiptChunk := chunk
	receiptChunk.Key = "receipt"
	receipt := snapshotAttempt("receipt", "wrapper", "wrapper", receiptChunk)
	receipt.Receipt = &Receipt{Thread: "captured", Calls: []string{"second", "first"}}
	snapshot := snapshotFixture(t, first, child, receipt, snapshotAttempt("empty", "", "", chunk))
	if got := snapshot.Receipts(); len(got) != 1 || !slices.Equal(got[0].Calls, []string{"second", "first"}) {
		t.Fatalf("receipt call order changed: %+v", got)
	}
	for _, tt := range []struct{ thread, item, page string }{
		{"main", "shared", "changehistory:" + chunk.Key}, {"child", "shared", "changehistory:child"},
		{"captured", "second", "changehistory:receipt"}, {"captured", "first", "changehistory:receipt"},
		{"wrapper", "wrapper", ""}, {"main", "first", ""}, {"other", "shared", ""}, {"", "", ""},
	} {
		t.Run(tt.thread+"/"+tt.item, func(t *testing.T) {
			got := snapshot.ActivityDetail(activitydetail.Request{Operation: activitydetail.Operation{Thread: tt.thread, Item: tt.item}})
			if tt.page == "" {
				if len(got.Pages) != 0 || got.Selected != "" {
					t.Fatalf("unmatched identity returned detail: %+v", got)
				}
			} else if len(got.Pages) != 1 || got.Pages[0].ID != tt.page || got.Selected != tt.page {
				t.Fatalf("identity resolved %+v, want %q", got, tt.page)
			}
		})
	}
}

func TestSnapshotActivityDetailTargetSelection(t *testing.T) {
	first := liveDiffNetCapture(1, "/root", "/w/docs/a", "/w/docs/a", "old\n", "new\n")
	move := liveDiffNetCapture(2, "/root", "/w/src/old.go", "/w/src/new.go", "old\n", "new\n")
	other := liveDiffNetCapture(3, "/root", "/w/src/other.go", "/w/src/other.go", "old\n", "new\n")
	attempt := snapshotAttempt("files", "thread", "item", first, move, other)
	snapshot := snapshotFixture(t, attempt)
	for _, tt := range []struct {
		path      string
		directory bool
		selected  string
	}{
		{"", false, first.Key}, {"docs/a", false, first.Key}, {"src/new.go", false, move.Key},
		{pathdisplay.Move("/w", move.Review.BeforePath, move.Review.AfterPath), false, move.Key},
		{"src/", true, move.Key}, {"src", false, ""}, {"sr", true, ""}, {"missing", false, ""},
	} {
		t.Run(tt.path, func(t *testing.T) {
			got := snapshot.ActivityDetail(activitydetail.Request{Operation: attempt.Operation, Path: tt.path, Directory: tt.directory})
			if tt.selected == "" {
				if len(got.Pages) != 0 || got.Selected != "" {
					t.Fatalf("unmatched target returned detail: %+v", got)
				}
				return
			}
			if len(got.Pages) != 3 || got.Selected != "changehistory:"+tt.selected {
				t.Fatalf("target selection: %+v", got)
			}
			page := got.Pages[1]
			if page.Verb != "Move" || page.Path != "src/new.go" || page.Language != "diff" || page.Code != move.Review.UnifiedDiffWithPaths("src/old.go", "src/new.go") {
				t.Fatalf("move page lost captured paths or diff: %+v", page)
			}
		})
	}
}

func TestSnapshotPublicationCountsIgnoreViewState(t *testing.T) {
	snapshot := snapshotFixture(t, snapshotAttempt("captures", "thread", "item",
		liveDiffNetCapture(1, "/root", "/w/a", "/w/a", "old\n", "draft\nextra\n"),
		liveDiffNetCapture(2, "/root/worker", "/w/a", "/w/a", "draft\nextra\n", "final\n"),
		liveDiffNetCapture(3, "/root/worker", "", "/w/b", "", "second\n"),
		liveDiffNetCapture(4, "/root", "", "/tmp/scratch", "", "scratch\n"),
	))
	publication := snapshot.Publication
	want := diffmodel.Counts{Added: 2, Removed: 1}
	wantCallers := map[string]diffmodel.Counts{"/root": {Added: 2, Removed: 1}, "/root/worker": {Added: 2, Removed: 2}}
	var view View
	view.Update(publication)
	view.Selected = 1
	for _, caller := range []string{"/root/worker", "/root", "missing", ""} {
		view.FilterCaller(caller)
		if view.Selected != 1 || publication.Counts.Net == nil || *publication.Counts.Net != want || !reflect.DeepEqual(publication.Counts.Callers, wantCallers) {
			t.Fatalf("view changed published counts or selection: caller=%q counts=%+v", caller, publication.Counts)
		}
		if got := NetCounts(&view); got == nil || *got != want {
			t.Fatalf("view disagrees with published net counts: %+v", got)
		}
	}
	if !reflect.DeepEqual(view.Visible, publication.Unfiltered) {
		t.Fatal("clearing caller filter did not restore the published composition")
	}
}
