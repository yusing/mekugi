package router

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/libraries/diffmodel"
	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
)

func TestLiveDiffScopeEventsRetainMembership(t *testing.T) {
	broker := newLivePreview(t.Context())
	scope := previewmodel.Scope{Workspaces: map[string]map[string]bool{"/work": {"root": true}}}
	setLiveScope(broker, scope)
	sub := testReview(broker).Subscribe()
	initial := <-sub.Events()
	scope.Workspaces["/work"]["child"] = true
	setLiveScope(broker, scope)
	updated := <-sub.Events()
	if initial.Scope.Workspaces["/work"]["child"] || !updated.Scope.Workspaces["/work"]["child"] {
		t.Fatal("scope publication changed an already queued membership snapshot")
	}
}

func TestLiveDiffSubscriberOverflowAndIsolation(t *testing.T) {
	broker := newLivePreview(t.Context())
	setLiveScope(broker, previewmodel.Scope{Workspaces: map[string]map[string]bool{"/work": {"root": true}}})
	sub := testReview(broker).Subscribe()
	<-sub.Events()
	testReview(broker).Publish([]reviewChange{{Workspace: "/work", Thread: "other"}})
	if len(sub.Events()) != 0 {
		t.Fatal("another session leaked into the stream")
	}
	for range 100 {
		testReview(broker).Publish([]reviewChange{{Workspace: "/work", Thread: "root"}})
	}
	select {
	case <-sub.Gap():
	default:
		t.Fatal("slow subscriber silently lost events")
	}
	next := testReview(broker).Subscribe()
	if event := <-next.Events(); event.Kind != "scope" {
		t.Fatal("reconnection did not establish a snapshot barrier")
	}
}

func TestLiveDiffPreviewMailboxCoalescesWithoutDelayingReceipts(t *testing.T) {
	broker := newLivePreview(t.Context())
	setLiveScope(broker, previewmodel.Scope{Workspaces: map[string]map[string]bool{"/workspace": {"thread": true}}})
	sub := testReview(broker).Subscribe()
	<-sub.Events()
	for i := 1; i <= 200; i++ {
		broker.Update(previewViewFixture("one", i), false)
	}
	testReview(broker).Publish([]reviewChange{{Workspace: "/workspace", Thread: "thread", ID: "amber1"}})
	if event := <-sub.Events(); event.Kind != "change" {
		t.Fatal("preview burst delayed durable publication")
	}
	select {
	case <-sub.Gap():
		t.Fatal("replaceable preview frames overflowed the durable queue")
	default:
	}
	updates := testReview(broker).TakePreviews(sub)
	if len(updates) != 1 || updates[0].Preview == nil || !strings.Contains(updates[0].Preview.Input, "stream_0200") {
		t.Fatalf("mailbox did not retain only latest snapshot: %+v", updates)
	}
	broker.Update(previewViewFixture("one", 201), false)
	broker.Update(previewmodel.Preview{ID: "one"}, true)
	updates = testReview(broker).TakePreviews(sub)
	if len(updates) != 1 || updates[0].Preview == nil || updates[0].Preview.Workspace != "" {
		t.Fatal("completion did not supersede pending frames")
	}
}

func TestLiveDiffPreviewMailboxPreservesExpandedScopeBarrier(t *testing.T) {
	broker := newLivePreview(t.Context())
	setLiveScope(broker, previewmodel.Scope{Workspaces: map[string]map[string]bool{"/workspace": {"old": true}}})
	sub := testReview(broker).Subscribe()
	<-sub.Events() // The HTTP writer has sent the initial scope and is paused.
	setLiveScope(broker, previewmodel.Scope{Workspaces: map[string]map[string]bool{"/workspace": {"old": true, "thread": true}}})
	broker.Update(previewViewFixture("new-thread", 20), false)
	// Force the preview-ready branch rather than relying on select scheduling.
	<-sub.Ready()
	batch := testReview(broker).TakePreviews(sub)
	if len(batch) != 2 || batch[0].Kind != "scope" || batch[1].Preview == nil {
		t.Fatalf("preview overtook its authorizing scope: %+v", batch)
	}
	if !batch[0].Scope.Workspaces[batch[1].Preview.Workspace][batch[1].Preview.Thread] {
		t.Fatal("preview is outside the preceding scope")
	}
}

func TestScopeCapacity(t *testing.T) {
	b := newSavedReview(t.Context(), nil)
	for _, workspace := range []string{"/tmp/a", "/tmp/with\"quote"} {
		for _, thread := range []string{"root", "child\nwith\\escape"} {
			b.Observe(workspace, thread, "", true)
			data, err := json.Marshal(b.scope)
			if err != nil || len(data) != b.scopeBytes {
				t.Fatalf("scope accounting: %d != %d (%v)", len(data), b.scopeBytes, err)
			}
		}
	}
	b.Observe("/tmp/oversized", strings.Repeat("x", maxReviewScopeBytes), "", true)
	if !b.exhausted || b.scope.Workspaces != nil {
		t.Fatal("scope exhaustion retained auxiliary collection")
	}
	b.Observe("/tmp/after", "new", "", true)
	if b.scope.Workspaces != nil {
		t.Fatal("disabled collection resumed")
	}
}

// Saved review and captured Activity receipts use the same application join
// whether or not livepreview is composed into the attached session.
func TestSavedReviewWithoutLivePreview(t *testing.T) {
	workspace := t.TempDir()
	writer, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, seed := range []struct{ thread, path string }{{"main", "root.go"}, {"child", "child.go"}, {"unrelated", "foreign.go"}, {"next", "next.go"}} {
		liveDiffScopeCapture(t, writer, workspace, seed.thread, seed.path, filepath.Join(workspace, seed.path), "before", "after")
	}
	u := newAppServerSessionTestUI(t, workspace)
	// Reopen retained evidence instead of relying on the publishing process cache.
	u.shell.diff.store = testReplayStore(mekugiReplayStore{directory: writer.directory})
	review := u.shell.review
	writer.liveDiff = review.Publish
	sub := review.Subscribe()
	apply := func() {
		t.Helper()
		for _, event := range review.TakePreviews(sub) {
			u.shell.applyDiff(t.Context(), event)
		}
		if u.shell.diffFailure != "" {
			t.Fatal(u.shell.diffFailure)
		}
	}
	paths := func() []string {
		var result []string
		for _, file := range u.shell.diff.data.files() {
			result = append(result, filepath.Base(file.Path))
		}
		slices.Sort(result)
		return result
	}
	u.restoreDiffThread(appServerThreadInfo{ID: "main", Cwd: workspace}, true)
	u.restoreDiffThread(appServerThreadInfo{ID: "child", Cwd: workspace}, false)
	apply()
	if got := paths(); !slices.Equal(got, []string{"child.go", "root.go"}) {
		t.Fatalf("restored saved Diff: %v", got)
	}
	u.shell.selectNativePane(1)
	if !u.shell.diffOpen {
		t.Fatal("saved Diff is not accessible")
	}

	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "turn", "item": map[string]any{
		"id": "format", "type": "commandExecution", "command": "gofmt -w formatted.go", "exitCode": 2,
	}})
	const call = "format\x00exec"
	id, err := writer.changes.Reserve(t.Context(), writer.scoped(t.Context()).changeScope(), workspace, "main", call)
	if err != nil {
		t.Fatal(err)
	}
	history := mekugiHistory{ToolName: nativeExecCommandToolName, Root: workspace, ExecutingThread: "main", CorrelationID: call, ChangeID: id,
		ReviewFiles: []diffmodel.ReviewFile{diffmodel.RenderReviewFile(filepath.Join(workspace, "formatted.go"), filepath.Join(workspace, "formatted.go"), "old\n", "new\n")},
		ExecOutcome: &execOutcome{Status: execStatusFailed, Coverage: execCoverageExact, Exit: new(2)},
	}
	if err := writer.put(t.Context(), workspace, map[string]mekugiHistory{call: history}); err != nil {
		t.Fatal(err)
	}
	apply()
	if got := paths(); !slices.Equal(got, []string{"child.go", "formatted.go", "root.go"}) {
		t.Fatalf("published saved Diff: %v", got)
	}
	assertCapturedCommand(t, u.view, "main", "format", "Edit", 2)
	foundReceipt := false
	for _, entry := range u.view.entries {
		if entry.native != nil && entry.native.item == "format" {
			foundReceipt = entry.native.capturedEdit != nil
		}
	}
	if !foundReceipt {
		t.Fatal("retained change did not reach its Activity command")
	}

	u.resetDiffScope()
	select {
	case <-sub.Gap():
	default:
		t.Fatal("root switch retained old mailbox")
	}
	u.restoreDiffThread(appServerThreadInfo{ID: "next", Cwd: workspace}, true)
	observeReview(review, workspace, "old-child", codexTurnMetadata{RequestKind: "turn", ParentThreadID: "main", SubagentKind: "review"})
	observeReview(review, workspace, "new-child", codexTurnMetadata{RequestKind: "turn", ParentThreadID: "next", SubagentKind: "review"})
	sub = review.Subscribe()
	apply()
	if got := paths(); !slices.Equal(got, []string{"next.go"}) {
		t.Fatalf("switched saved Diff: %v", got)
	}
	scope := review.Scope().Workspaces[workspace]
	if scope["main"] || scope["old-child"] || !scope["new-child"] {
		t.Fatalf("switch admission: %+v", scope)
	}
	if review.Preview() != nil {
		t.Fatal("saved review constructed an omitted preview feature")
	}
}

func TestSavedReviewSwitchRetiresOnlyPreviewAttachment(t *testing.T) {
	review := newSavedReview(t.Context(), new(previewmodel.Service))
	defer review.Close()
	review.IncludeThread("/workspace", "main", true)
	old := review.Preview()
	old.Enable()
	frame := previewViewFixture("old", 1)
	frame.Thread = "main"
	old.Update(frame, false)
	if len(old.Current()) != 1 {
		t.Fatal("preview fixture was not admitted")
	}
	worker := startLiveDiffPreview(t.Context(), old, "/workspace", "main")
	windows := new(execWindowRegistry)
	windows.open(&execWindow{ref: "host-cell", thread: "main", turn: "turn"})
	review.ResetScope()
	review.IncludeThread("/workspace", "next", true)
	select {
	case <-worker.done:
	case <-time.After(time.Second):
		t.Fatal("retired attachment kept its input projection worker")
	}
	next := review.Preview()
	if next == old || old.Context().Err() == nil || next.Context().Err() != nil || len(next.Current()) != 0 || !next.Enabled() {
		t.Fatal("session switch retained old preview ownership")
	}
	if window := windows.find("host-cell"); window == nil || !window.closed.IsZero() || t.Context().Err() != nil {
		t.Fatal("preview retirement finalized capture or canceled host authority")
	}
}
