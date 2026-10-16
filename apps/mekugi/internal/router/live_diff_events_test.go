package router

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"

	"github.com/yusing/mekugi/libraries/diffrender"

	"github.com/yusing/mekugi/libraries/legacycall"
	"github.com/yusing/mekugi/modules/changehistory/changes"

	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	changeview "github.com/yusing/mekugi/modules/changehistory/terminalview"
	previewview "github.com/yusing/mekugi/modules/livepreview/terminalview"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func liveDiffTestBroker(t *testing.T, store *mekugiReplayStore, scope previewmodel.Scope) (*previewmodel.Session, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	broker := newLivePreview(ctx)
	setLiveScope(broker, scope)
	store.liveDiff = testReview(broker).Publish
	return broker, cancel
}

func waitLiveDiffChange(t *testing.T, sub *reviewSubscription) reviewChange {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event := <-sub.Events():
			if event.Kind == "change" && len(event.Changes) != 0 {
				return event.Changes[0]
			}
		case <-sub.Gap():
			t.Fatal("subscriber overflowed")
		case <-timer.C:
			t.Fatal("missing direct publication event")
		}
	}
}

func TestLiveDiffDirectPublicationAndDurableReload(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	broker, _ := liveDiffTestBroker(t, store, previewmodel.Scope{Workspaces: map[string]map[string]bool{workspace: {"identity-thread": true}}})
	sub := testReview(broker).Subscribe()
	liveDiffIdentityCapture(t, store, workspace, "one", "", "file.txt", "", "original\n")
	event := waitLiveDiffChange(t, sub)
	data := newLiveDiffData()
	if err := data.apply(t.Context(), store, event); err != nil {
		t.Fatal(err)
	}
	files := data.files()
	if len(files) != 1 || len(files[0].Chunks) != 1 || !strings.Contains(files[0].Chunks[0].Review.Diff, "+original") {
		t.Fatalf("publication lost saved edit: %+v", files)
	}
	callID := event.Calls[0]
	if err := os.Remove(filepath.Join(store.directory, legacycall.Name(workspace, callID, false))); err != nil {
		t.Fatal(err)
	}
	if _, err := store.liveDiffSnapshot(t.Context(), previewmodel.Scope{Workspaces: map[string]map[string]bool{workspace: {"identity-thread": true}}}); err == nil {
		t.Fatal("reconnection hid missing durable evidence")
	}
}

func waitLiveDiffEvent(t *testing.T, sub *reviewSubscription, match func(reviewEvent) bool) reviewEvent {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event := <-sub.Events():
			if match(event) {
				return event
			}
		case <-sub.Gap():
			t.Fatal("unexpected subscriber gap")
		case <-timer.C:
			t.Fatal("missing live diff event")
		}
	}
}

func TestLiveDiffScopeReconciliationUsesCachedAttempts(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	scope := previewmodel.Scope{Workspaces: map[string]map[string]bool{workspace: {"identity-thread": true}}}
	liveDiffIdentityCapture(t, store, workspace, "one", "", "file.txt", "", "original\n")
	data, err := store.liveDiffSnapshot(t.Context(), previewmodel.Scope{Workspaces: map[string]map[string]bool{workspace: {"identity-thread": true}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(store.directory, legacycall.Name(workspace, "one", false))); err != nil {
		t.Fatal(err)
	}
	if err := data.reconcile(t.Context(), store, scope); err != nil {
		t.Fatalf("unchanged membership reread an immutable capture: %v", err)
	}
	index, err := store.changes.Inspect(store.changeScope(), workspace)
	if err != nil {
		t.Fatal(err)
	}
	index.Changes["amber1"] = changes.Change{Correlation: "one"}
	if err := store.locked(t.Context(), func(store *mekugiReplayStore) error { return writeChangeIndexFixture(store, index) }); err != nil {
		t.Fatal(err)
	}
	if err := data.reconcile(t.Context(), store, scope); err == nil {
		t.Fatal("reconciliation hid removed membership")
	}
}

func TestLiveDiffJSONBatchKeepsFollowAndRecency(t *testing.T) {
	transform, proxy, _, workspace := newMekugiTestTransform(t)
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	proxy.replayStore = store
	broker, _ := liveDiffTestBroker(t, store, previewmodel.Scope{
		Workspaces: map[string]map[string]bool{workspace: {transform.shellThreadID: true}},
	})
	sub := testReview(broker).Subscribe()
	<-sub.Events()
	histories := make(map[string]mekugiHistory)
	for _, name := range []string{"first", "second"} {
		id, err := store.changes.Reserve(t.Context(), store.scoped(t.Context()).changeScope(), workspace, transform.shellThreadID, name)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(workspace, name+".txt"), nil, 0600); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(workspace, name+".txt")
		if err := os.WriteFile(path, []byte("content\n"), 0600); err != nil {
			t.Fatal(err)
		}
		histories[name] = mekugiHistory{ToolName: applyPatchToolName, ChangeID: id, CorrelationID: name, ReviewFiles: []diffmodel.ReviewFile{diffmodel.RenderReviewFile(path, path, "", "content\n")}}
	}
	if err := store.put(t.Context(), workspace, histories); err != nil {
		t.Fatal(err)
	}
	event := waitLiveDiffEvent(t, sub, func(e reviewEvent) bool { return e.Kind == "change" })
	if len(event.Changes) != 2 || len(sub.Events()) != 0 {
		t.Fatalf("one durable batch was split into display updates: %+v", event)
	}
	data := newLiveDiffData()
	view := changeview.View{}
	view.Merge(nil) // The connected pane starts empty before this batch.
	for _, change := range event.Changes {
		if err := data.apply(t.Context(), store, change); err != nil {
			t.Fatal(err)
		}
	}
	view.Merge(data.files())
	view.RefreshVisible()
	for _, file := range view.Files {
		if !view.Visible[file.Key()].Highlighted {
			t.Fatal("durable batch lost a file's recency marker")
		}
	}
	// Replay of that unchanged response does not create another display update.
	if err := store.put(t.Context(), workspace, histories); err != nil {
		t.Fatal(err)
	}
	if len(sub.Events()) != 0 {
		t.Fatal("unchanged replay emitted a new update")
	}
}

func TestUISnapshotLiveDiffRequiresHunk(t *testing.T) {
	workspace := t.TempDir()
	broker := newLivePreview(t.Context())
	setLiveScope(broker, previewmodel.Scope{Workspaces: map[string]map[string]bool{workspace: {"thread": true}}})
	sub := testReview(broker).Subscribe()
	var pane previewview.PreviewPane
	worker := liveDiffPreviewWorker{ctx: t.Context(), kind: applyPatchToolName}
	for _, input := range []string{
		"*** Begin Patch\n*** Add File: empty.txt\n",
		"*** Begin Patch\n*** Add File: empty.txt\n*** End Patch\n",
	} {
		preview, ok := worker.project(input, workspace, strings.Contains(input, "*** End Patch"))
		if !ok {
			t.Fatal("patch not recognized")
		}
		preview.ID, preview.Workspace, preview.Thread = "patch", workspace, "thread"
		broker.Update(preview, false)
		if len(broker.Current()) != 0 {
			t.Fatalf("hunkless patch opened live diff: %+v", preview)
		}
		for _, event := range testReview(broker).TakePreviews(sub) {
			if event.Preview != nil {
				pane.Update(*event.Preview)
			}
		}
	}
	if len(pane.Views) != 0 {
		t.Fatal("hunkless patch reserved live UI space")
	}
	valid := diffmodel.RenderReviewFile("", filepath.Join(workspace, "valid.txt"), "", "content\n")
	preview := previewmodel.Preview{ID: "mixed", Workspace: workspace, Thread: "thread", Status: previewmodel.PreviewRunning,
		Files: []diffmodel.ReviewFile{diffmodel.RenderReviewFile("", filepath.Join(workspace, "empty.txt"), "", ""), valid}}
	broker.Update(preview, false)
	if shown, _ := currentLivePreview(broker, preview.ID); len(shown.Files) != 1 || shown.Files[0] != valid || len(preview.Files) != 2 {
		t.Fatalf("mixed admission lost a valid hunk or changed evidence: %+v", shown)
	}
	for _, event := range testReview(broker).TakePreviews(sub) {
		pane.Update(*event.Preview)
	}
	rows, err := pane.RenderBatch(t.Context(), "", workspace, diffrender.DarkTheme, 60, 16, 15)
	if err != nil {
		t.Fatal(err)
	}
	uisnapshot.Assert(t, "testdata/snapshots/live-diff-hunk-admission.txt", strings.Join(rows, "\n"))
}

func TestLiveDiffPreviewScriptPayloadBound(t *testing.T) {
	broker := newLivePreview(t.Context())
	setLiveScope(broker, previewmodel.Scope{Workspaces: map[string]map[string]bool{"/workspace": {"thread": true}}})
	broker.Update(previewmodel.Preview{ID: "large-script", Workspace: "/workspace", Thread: "thread",
		Input: strings.Repeat("界\n", 50000) + "TAIL"}, false)
	preview, _ := currentLivePreview(broker, "large-script")
	if !preview.Truncated || len(responses.MustMarshalJSON(preview)) > 48<<10 ||
		!strings.HasSuffix(preview.Input, "TAIL") || strings.Contains(preview.Status, "UNAVAILABLE") {
		t.Fatalf("script tail was not retained within the display bound: bytes=%d", len(responses.MustMarshalJSON(preview)))
	}
}

func TestLiveDiffTurnEventValidation(t *testing.T) {
	for _, status := range []string{"active", "completed"} {
		if err := validateLiveDiffEvent(reviewEvent{Kind: "turn", Status: status, TurnRevision: 1}); err != nil {
			t.Fatalf("%s: %v", status, err)
		}
	}
	if err := validateLiveDiffEvent(reviewEvent{Kind: "turn", Status: "unknown", TurnRevision: 1}); err == nil {
		t.Fatal("accepted unknown turn status")
	}
	if err := validateLiveDiffEvent(reviewEvent{Kind: "turn", Status: "active"}); err == nil {
		t.Fatal("accepted turn event without revision")
	}
}

func TestLiveDiffBoundedPreviewWorkspacePaths(t *testing.T) {
	for _, path := range []string{"/workspace/src/main.go", "/outside/main.go"} {
		preview := boundPreview(previewmodel.Preview{Workspace: "/workspace", Files: []diffmodel.ReviewFile{
			diffmodel.RenderReviewFile("", "/workspace/large.txt", "", strings.Repeat("padding\n", 10000)),
			diffmodel.RenderReviewFile(path, path, "const x = 1\n", "const x = 2\n"),
		}})
		want := path
		if path == "/workspace/src/main.go" {
			want = "src/main.go"
		}
		if !preview.DiffText || !strings.Contains(preview.Input, "+++ \""+want+"\"") || strings.Contains(preview.Input, "/workspace/") {
			t.Fatalf("bad display paths: %q", preview.Input)
		}
	}
}

func TestLiveDiffBoundedPreviewOmitsDeletedFiles(t *testing.T) {
	deleted := diffmodel.RenderReviewFile("/workspace/deleted.go", "", strings.Repeat("old source\n", 10000), "")
	for _, mixed := range []bool{false, true} {
		preview := previewmodel.Preview{ID: "call", Workspace: "/workspace", Caller: "/root", Status: previewmodel.PreviewEdit, Files: []diffmodel.ReviewFile{deleted}}
		if mixed {
			preview.Files = append(preview.Files, diffmodel.RenderReviewFile("", "/workspace/kept.go", "", "package main\n"))
		}
		bounded := boundPreview(preview)
		if strings.Contains(bounded.Input, "deleted.go") || strings.Contains(bounded.Input, "old source") {
			t.Fatal("deleted rows survived tail conversion")
		}
		if preview.Files[0] != deleted {
			t.Fatal("original deletion evidence changed")
		}
		var pane previewview.PreviewPane
		pane.Update(bounded)
		rows, err := pane.RenderBatch(t.Context(), "/root", "/workspace", diffrender.DarkTheme, 72, 31, 15)
		if err != nil {
			t.Fatal(err)
		}
		if mixed {
			if len(rows) == 0 || len(pane.Callers()) != 1 {
				t.Fatal("mixed preview lost nondeleted source")
			}
		} else if len(rows) != 0 || len(pane.Callers()) != 0 {
			t.Fatal("oversized deletion opened live dock")
		}
	}
}
