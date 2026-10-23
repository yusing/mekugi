package sessionreview

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
)

// Source: apps/mekugi/internal/router/saved_review_test.go@cf02d9b2.
func TestLiveDiffScopeEventsRetainMembership(t *testing.T) {
	review := New(t.Context(), previewmodel.New(changecapture.ReadFile), nil)
	scope := previewmodel.Scope{Workspaces: map[string]map[string]bool{"/work": {"root": true}}}
	setScope(review, scope)
	sub := review.Subscribe()
	initial := <-sub.Events()
	scope.Workspaces["/work"]["child"] = true
	setScope(review, scope)
	updated := <-sub.Events()
	if initial.Scope.Workspaces["/work"]["child"] || !updated.Scope.Workspaces["/work"]["child"] {
		t.Fatal("scope publication changed an already queued membership snapshot")
	}
}

func TestLiveDiffSubscriberOverflowAndIsolation(t *testing.T) {
	review := New(t.Context(), previewmodel.New(changecapture.ReadFile), nil)
	setScope(review, previewmodel.Scope{Workspaces: map[string]map[string]bool{"/work": {"root": true}}})
	sub := review.Subscribe()
	<-sub.Events()
	review.Publish([]changes.Update{{Workspace: "/work", Thread: "other"}})
	if len(sub.Events()) != 0 {
		t.Fatal("another session leaked into the stream")
	}
	for range 100 {
		review.Publish([]changes.Update{{Workspace: "/work", Thread: "root"}})
	}
	select {
	case <-sub.Gap():
	default:
		t.Fatal("slow subscriber silently lost events")
	}
	next := review.Subscribe()
	if event := <-next.Events(); event.Kind != "scope" {
		t.Fatal("reconnection did not establish a snapshot barrier")
	}
}

func TestLiveDiffPreviewMailboxCoalescesWithoutDelayingReceipts(t *testing.T) {
	review := New(t.Context(), previewmodel.New(changecapture.ReadFile), nil)
	broker := review.Preview()
	setScope(review, previewmodel.Scope{Workspaces: map[string]map[string]bool{"/workspace": {"thread": true}}})
	sub := review.Subscribe()
	<-sub.Events()
	for i := 1; i <= 200; i++ {
		broker.Update(previewViewFixture("one", i), false)
	}
	review.Publish([]changes.Update{{Workspace: "/workspace", Thread: "thread", ID: "amber1"}})
	if event := <-sub.Events(); event.Kind != "change" {
		t.Fatal("preview burst delayed durable publication")
	}
	select {
	case <-sub.Gap():
		t.Fatal("replaceable preview frames overflowed the durable queue")
	default:
	}
	updates := review.TakePreviews(sub)
	if len(updates) != 1 || updates[0].Preview == nil || !strings.Contains(updates[0].Preview.Input, "stream_0200") {
		t.Fatalf("mailbox did not retain only latest snapshot: %+v", updates)
	}
	broker.Update(previewViewFixture("one", 201), false)
	broker.Update(previewmodel.Preview{ID: "one"}, true)
	updates = review.TakePreviews(sub)
	if len(updates) != 1 || updates[0].Preview == nil || updates[0].Preview.Workspace != "" {
		t.Fatal("completion did not supersede pending frames")
	}
}

func TestLiveDiffPreviewMailboxPreservesExpandedScopeBarrier(t *testing.T) {
	review := New(t.Context(), previewmodel.New(changecapture.ReadFile), nil)
	broker := review.Preview()
	setScope(review, previewmodel.Scope{Workspaces: map[string]map[string]bool{"/workspace": {"old": true}}})
	sub := review.Subscribe()
	<-sub.Events() // The subscriber consumed the initial scope and is paused.
	setScope(review, previewmodel.Scope{Workspaces: map[string]map[string]bool{"/workspace": {"old": true, "thread": true}}})
	broker.Update(previewViewFixture("new-thread", 20), false)
	// Force the preview-ready branch rather than relying on select scheduling.
	<-sub.Ready()
	batch := review.TakePreviews(sub)
	if len(batch) != 2 || batch[0].Kind != "scope" || batch[1].Preview == nil {
		t.Fatalf("preview overtook its authorizing scope: %+v", batch)
	}
	if !batch[0].Scope.Workspaces[batch[1].Preview.Workspace][batch[1].Preview.Thread] {
		t.Fatal("preview is outside the preceding scope")
	}
}

func TestScopeCapacity(t *testing.T) {
	b := New(t.Context(), previewmodel.New(changecapture.ReadFile), nil)
	defer b.Close()
	b.Preview().Enable()
	for _, workspace := range []string{"/tmp/a", "/tmp/with\"quote"} {
		for _, thread := range []string{"root", "child\nwith\\escape"} {
			b.Observe(workspace, thread, "", true)
			data, err := json.Marshal(b.scope)
			if err != nil || len(data) != b.scopeBytes {
				t.Fatalf("scope accounting: %d != %d (%v)", len(data), b.scopeBytes, err)
			}
		}
	}
	sub := b.Subscribe()
	b.Preview().Update(previewmodel.Preview{ID: "edit", Workspace: "/tmp/a", Thread: "root", Input: "source", Status: "streaming"}, false)
	if len(b.Preview().Current()) != 1 {
		t.Fatal("preview was not admitted before scope exhaustion")
	}
	b.Observe("/tmp/oversized", strings.Repeat("x", maxReviewScopeBytes), "", true)
	if !b.exhausted || b.scope.Workspaces != nil {
		t.Fatal("scope exhaustion retained auxiliary collection")
	}
	if b.Preview().Enabled() || len(b.Preview().Current()) != 0 {
		t.Fatal("withdrawn scope retained publishable previews")
	}
	for _, event := range b.TakePreviews(sub) {
		if event.Preview != nil {
			t.Fatal("withdrawn scope retained a queued preview")
		}
	}
	b.Observe("/tmp/after", "new", "", true)
	if b.scope.Workspaces != nil {
		t.Fatal("disabled collection resumed")
	}
}

func setScope(review *Review, scope previewmodel.Scope) {
	review.Preview().Enable()
	for workspace, threads := range scope.Workspaces {
		for thread, included := range threads {
			if included {
				review.IncludeThread(workspace, thread, true)
			}
		}
	}
}
func previewViewFixture(id string, rows int) previewmodel.Preview {
	return previewmodel.Preview{ID: id, Workspace: "/workspace", Thread: "thread", Input: fmt.Sprintf("+stream_%04d\n", rows)}
}
