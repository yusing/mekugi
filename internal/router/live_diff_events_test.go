package router

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"

	"github.com/yusing/mekugi/libraries/diffrender"

	"github.com/yusing/mekugi/libraries/legacycall"
	"github.com/yusing/mekugi/modules/changehistory/changes"

	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/ui/diffview"
	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func liveDiffTestBroker(t *testing.T, store *mekugiReplayStore, scope previewmodel.Scope) (string, *previewmodel.Session, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	broker := newLivePreview(ctx)
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+liveDiffEventsPath, func(w http.ResponseWriter, r *http.Request) { serveLiveEvents(broker, w, r) })
	server := httptest.NewServer(mux)
	t.Cleanup(func() { cancel(); server.Close() })
	setLiveEndpoint(broker, server.URL+liveDiffEventsPath)
	setLiveScope(broker, scope)
	store.liveDiff = testReview(broker).Publish
	connection := filepath.Join(t.TempDir(), "connection.json")
	data, err := json.Marshal(liveDescriptor(broker))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(connection, data, 0600); err != nil {
		t.Fatal(err)
	}
	return connection, broker, cancel
}

func liveDiffTestSession(t *testing.T, store *mekugiReplayStore, workspace string) string {
	t.Helper()
	path, _, _ := liveDiffTestBroker(t, store, previewmodel.Scope{
		Workspaces: map[string]map[string]bool{workspace: {"thread": true, "identity-thread": true, "flush-thread": true}},
	})
	return path
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
	_, broker, _ := liveDiffTestBroker(t, store, previewmodel.Scope{Workspaces: map[string]map[string]bool{workspace: {"identity-thread": true}}})
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

func TestLiveDiffStreamReconnectSnapshotBarrier(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("Authorization") != "Bearer test" {
			t.Error("viewer omitted its private capability")
		}
		encoder := json.NewEncoder(w)
		scope := previewmodel.Scope{Workspaces: map[string]map[string]bool{}}
		_ = encoder.Encode(reviewEvent{Kind: "scope", Scope: &scope, Resync: true})
		if requests == 1 {
			_ = encoder.Encode(reviewEvent{Kind: "reset"})
		} else {
			_ = encoder.Encode(reviewEvent{Kind: "end"})
		}
	}))
	defer server.Close()
	output := make(chan reviewEvent, 32)
	liveDiffStream(ctx, liveDiffConnection{Endpoint: server.URL, Token: "test"}, output)
	var kinds []string
	for event := range output {
		kinds = append(kinds, event.Kind)
		if event.Kind == "scope" && !event.Resync {
			t.Fatal("reconnect did not request a fresh snapshot")
		}
	}
	if fmt.Sprint(kinds) != "[scope coverage scope end]" {
		t.Fatalf("reconnect transitions: %v", kinds)
	}
}

func TestLiveDiffPublisherRequiresCapability(t *testing.T) {
	broker := newLivePreview(t.Context())
	response := httptest.NewRecorder()
	func(w http.ResponseWriter, r *http.Request) { serveLiveEvents(broker, w, r) }(response, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}\n")))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated connection accepted: %d %s", response.Code, response.Body.String())
	}
}

func TestLiveDiffJSONBatchKeepsFollowAndRecency(t *testing.T) {
	transform, proxy, _, workspace := newMekugiTestTransform(t)
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	proxy.replayStore = store
	_, broker, _ := liveDiffTestBroker(t, store, previewmodel.Scope{
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
	view := livediff.View{Following: true}
	view.Merge(nil) // The connected pane starts empty before this batch.
	for _, change := range event.Changes {
		if err := data.apply(t.Context(), store, change); err != nil {
			t.Fatal(err)
		}
	}
	view.Merge(data.files())
	view.RefreshVisible()
	if view.Files[view.Selected].Path != filepath.Join(workspace, "second.txt") {
		t.Fatal("FOLLOW selected an older call from the durable batch")
	}
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

func TestLiveDiffDelayedCaptureDoesNotFollowBackwards(t *testing.T) {
	path := "same.txt"
	older := liveDiffHighlightChunk("older", path, "@@ -20 +20 @@\n-old\n+OLDER20\n")
	older.CaptureOrder, older.SnapshotOrder = 1, 1
	newer := liveDiffHighlightChunk("newer", path, "@@ -90 +90 @@\n-old\n+NEWER90\n")
	newer.CaptureOrder, newer.SnapshotOrder = 2, 2
	view := livediff.View{Following: true}
	view.Merge([]livediff.File{{Path: path, Chunks: []livediff.Chunk{newer}}})
	view.Merge([]livediff.File{{Path: path, Chunks: []livediff.Chunk{older, newer}}})
	if view.Latest != "newer" || view.Files[view.Selected].Path != path {
		t.Fatal("late publication of an older capture moved FOLLOW backwards")
	}

}

type liveDiffPausedWriter struct {
	http.ResponseWriter
	ctx     context.Context
	ready   chan struct{}
	release chan struct{}
	paused  bool
}

func (w *liveDiffPausedWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *liveDiffPausedWriter) Flush() {
	w.ResponseWriter.(http.Flusher).Flush()
	if !w.paused {
		w.paused = true
		close(w.ready)
		select {
		case <-w.release:
		case <-w.ctx.Done():
		}
	}
}

func TestLiveDiffExpandedScopeBeforePreviewOnDelayedTransport(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	broker := newLivePreview(ctx)
	setLiveScope(broker, previewmodel.Scope{Workspaces: map[string]map[string]bool{"/workspace": {"old": true}}})
	ready, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		func(w http.ResponseWriter, r *http.Request) { serveLiveEvents(broker, w, r) }(&liveDiffPausedWriter{ResponseWriter: w, ctx: ctx, ready: ready, release: release}, r)
	}))
	t.Cleanup(func() { cancel(); server.Close() })
	setLiveEndpoint(broker, server.URL+liveDiffEventsPath)
	events := make(chan reviewEvent, 16)
	go liveDiffStream(ctx, liveDescriptor(broker), events)
	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal("initial snapshot did not reach the writer")
	}
	if event := <-events; event.Kind != "scope" {
		t.Fatal("missing initial scope")
	}
	setLiveScope(broker, previewmodel.Scope{Workspaces: map[string]map[string]bool{"/workspace": {"old": true, "thread": true}}})
	broker.Update(previewViewFixture("new-thread", 20), false)
	close(release)
	var scope *previewmodel.Scope
	for scope == nil {
		select {
		case event := <-events:
			if event.Kind != "scope" {
				t.Fatalf("preview overtook expanded scope: %+v", event)
			}
			scope = event.Scope
		case <-ctx.Done():
			t.Fatal("scope expansion was not delivered")
		}
	}
	select {
	case event := <-events:
		if event.Preview == nil || !scope.Workspaces[event.Preview.Workspace][event.Preview.Thread] {
			t.Fatalf("missing authorized preview after scope: %+v", event)
		}
	case <-ctx.Done():
		t.Fatal("preview was not delivered")
	}
}

func TestUISnapshotLiveDiffRequiresHunk(t *testing.T) {
	workspace := t.TempDir()
	broker := newLivePreview(t.Context())
	setLiveScope(broker, previewmodel.Scope{Workspaces: map[string]map[string]bool{workspace: {"thread": true}}})
	sub := testReview(broker).Subscribe()
	var pane diffview.PreviewPane
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
	rows, err := pane.Render(t.Context(), workspace, diffrender.DarkTheme, 60, 8)
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
		for _, retain := range []bool{false, true} {
			pane := diffview.PreviewPane{Retain: retain}
			pane.Update(bounded)
			var rows []string
			var err error
			if retain {
				rows, err = pane.RenderBatch(t.Context(), "/root", "/workspace", diffrender.DarkTheme, 72, 31, 15)
			} else {
				rows, err = pane.Render(t.Context(), "/workspace", diffrender.DarkTheme, 72, 31)
			}
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
}
