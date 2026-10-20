package presentation

import (
	"testing"
	"time"

	"github.com/yusing/mekugi/contracts/scope"
)

func TestSupplementRetentionUsesQueueTime(t *testing.T) {
	var invocation, thread scope.Store
	owner := RequestSupplements(&invocation)
	owner.Attach("main")
	original := time.Unix(1700000000, 0)
	queued := original.Add(24 * time.Hour)
	event := Supplement{Thread: "child", Source: "old-assignment", Kind: "assignment", Text: "Historical task", Observed: original}
	if owner.Collect(&thread, event, queued) {
		t.Fatal("event omitted")
	}
	// An unknown ancestor does not discard an otherwise admitted source.
	if got := owner.Take(Audience{Root: "main"}, queued.Add(time.Hour-time.Nanosecond)); len(got) != 0 {
		t.Fatal("unknown ancestry delivered")
	}
	audience := Audience{Root: "main", Threads: map[string]string{"child": "/root/reviewer"}}
	got := owner.Take(audience, queued.Add(time.Hour-time.Nanosecond))
	if len(got) != 1 || !got[0].Observed.Equal(original) {
		t.Fatalf("authored time changed or controlled expiry: %+v", got)
	}
	owner.Collect(&thread, event, queued.Add(time.Hour))
	if got := owner.Take(audience, queued.Add(time.Hour)); len(got) != 0 {
		t.Fatal("delivered source replayed")
	}
	event.Source = "expires"
	owner.Collect(&thread, event, queued)
	if got := owner.Take(audience, queued.Add(time.Hour)); len(got) != 0 {
		t.Fatal("queue did not expire")
	}
}

func TestSupplementStartDeduplicatesAssignmentAcrossAttachment(t *testing.T) {
	var invocation, thread scope.Store
	owner := RequestSupplements(&invocation)
	owner.Attach("main")
	now := time.Unix(1700000000, 0)
	assignment := &SupplementAssignment{ID: "task", From: "/root", To: "/root/child", Text: "Work"}
	owner.Collect(&thread, Supplement{Thread: "child", Source: "start", Kind: "start", Assignment: assignment, Start: &SupplementStart{Model: "model"}}, now)
	owner.Collect(&thread, Supplement{Thread: "child", Source: "task", Kind: "assignment", Assignment: assignment, Text: "Work"}, now)
	audience := Audience{Root: "main", Threads: map[string]string{"child": "/root/child"}}
	if got := owner.Take(audience, now); len(got) != 1 || got[0].Kind != "start" {
		t.Fatalf("spawn and first assignment split: %+v", got)
	}
	owner.Detach()
	owner.Attach("main")
	owner.Collect(&thread, Supplement{Thread: "child", Source: "task", Kind: "assignment", Assignment: assignment, Text: "Work"}, now)
	if got := owner.Take(audience, now); len(got) != 0 {
		t.Fatal("reattachment lost source deduplication")
	}
}

func TestSupplementEditReceiptRequiresHostIdentity(t *testing.T) {
	var invocation, thread scope.Store
	owner := RequestSupplements(&invocation)
	owner.Attach("main")
	now := time.Unix(1700000000, 0)
	event := Supplement{Thread: "main", Source: "receipt", Kind: SupplementEditReceipt, Text: "Edit `file.go` +1 -1"}
	owner.Collect(&thread, event, now)
	event.Calls = []string{"command", "sibling"}
	owner.Collect(&thread, event, now)
	owner.Collect(&thread, event, now)
	event.Calls[0] = "mutated"
	got := owner.Take(Audience{Root: "main", Threads: map[string]string{"main": "/root"}}, now)
	if len(got) != 1 || got[0].Kind != SupplementEditReceipt || got[0].Calls[0] != "command" || got[0].Calls[1] != "sibling" {
		t.Fatalf("receipt lost admission, identity or deduplication: %+v", got)
	}
}

func TestSupplementBorrowedAudiencePreservesIndependentDelivery(t *testing.T) {
	var invocation scope.Store
	owner := RequestSupplements(&invocation)
	owner.Attach("main")
	owner.Borrow("oneoff")
	now := time.Unix(100, 0)
	for _, root := range []string{"main", "oneoff"} {
		var thread scope.Store
		owner.Collect(&thread, Supplement{Thread: root, Source: root, Kind: "error", Text: root}, now)
		owner.StartRequest(root, now)
		owner.NoteUnreturned(root, []string{"call"})
	}
	for _, root := range []string{"main", "oneoff"} {
		audience := Audience{Root: root, Threads: map[string]string{root: "/root"}}
		if got := owner.Take(audience, now); len(got) != 1 || got[0].Thread != root {
			t.Fatalf("wrong supplement audience: %+v", got)
		}
		if got := owner.TakeStarts(audience); len(got) != 1 || got[0].Thread != root {
			t.Fatalf("wrong start audience: %+v", got)
		}
		if got := owner.TakeUnreturned(audience); len(got) != 1 || got[0].Thread != root {
			t.Fatalf("wrong unreturned audience: %+v", got)
		}
	}
	owner.Borrow("")
	if owner.Attached("oneoff") || !owner.Attached("main") {
		t.Fatal("releasing borrow changed Main attachment")
	}
}
