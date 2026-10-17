//go:build unix

package router

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"

	"github.com/yusing/mekugi/libraries/diffmodel"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
)

func savedReviewLoaderFixture(t *testing.T) (*savedReviewLoader, *mekugiReplayStore, string) {
	t.Helper()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state := savedDiff{store: store}
	state.reset()
	loader := newSavedReviewLoader(t.Context(), state)
	t.Cleanup(loader.close)
	return loader, store, t.TempDir()
}

func savedReviewLoaderScope(workspace string, resync bool, threads ...string) sessionreview.Event {
	members := make(map[string]bool)
	for _, thread := range threads {
		members[thread] = true
	}
	return sessionreview.Event{Kind: "scope", Resync: resync, Scope: &previewmodel.Scope{Workspaces: map[string]map[string]bool{workspace: members}}}
}

func savedReviewLoaderEnqueue(t *testing.T, loader *savedReviewLoader, event sessionreview.Event) {
	t.Helper()
	if err := loader.enqueue(event); err != nil {
		t.Fatal(err)
	}
}

func savedReviewLoaderResult(t *testing.T, loader *savedReviewLoader) savedReviewResult {
	t.Helper()
	select {
	case result := <-loader.updates():
		return result
	case <-time.After(5 * time.Second):
		t.Fatal("saved review did not finish retained read")
		return savedReviewResult{}
	}
}

func savedReviewLoaderAccept(t *testing.T, loader *savedReviewLoader) savedReviewResult {
	t.Helper()
	result := savedReviewLoaderResult(t, loader)
	if accepted, err := loader.accept(result); !accepted || err != nil {
		t.Fatalf("accept retained review: accepted=%v error=%v", accepted, err)
	}
	return result
}

func savedReviewLoaderBlock(t *testing.T, store *mekugiReplayStore) (*blockedReviewChanges, func()) {
	t.Helper()
	blocked := &blockedReviewChanges{Service: store.changes, entered: make(chan struct{}), release: make(chan struct{})}
	store.changes = blocked
	release := sync.OnceFunc(func() { close(blocked.release) })
	t.Cleanup(release)
	return blocked, release
}

func savedReviewLoaderEntered(t *testing.T, blocked *blockedReviewChanges) {
	t.Helper()
	select {
	case <-blocked.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("saved review did not enter retained read")
	}
}

func TestSavedReviewLoaderPublishesImmutableAtomicReceiptData(t *testing.T) {
	loader, store, workspace := savedReviewLoaderFixture(t)
	const call = "command\x00exec"
	id, err := store.changes.Reserve(t.Context(), store.scoped(t.Context()).changeScope(), workspace, "main", call)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(workspace, "receipt.go")
	history := mekugiHistory{ChangeID: id, CorrelationID: call, ExecutingThread: "main", ToolName: nativeExecCommandToolName,
		ReviewFiles: []diffmodel.ReviewFile{diffmodel.RenderReviewFile(path, path, "before\n", "after\n")},
		ExecOutcome: &changecapture.Outcome{Status: changecapture.StatusFailed, Coverage: changecapture.CoverageExact, Exit: new(2)}}
	if err := store.put(t.Context(), workspace, map[string]mekugiHistory{call: history}); err != nil {
		t.Fatal(err)
	}
	savedReviewLoaderEnqueue(t, loader, savedReviewLoaderScope(workspace, true, "main"))
	published := savedReviewLoaderAccept(t, loader)
	data := loader.state.data
	attempt := reviewSnapshotAttempt(t, data, workspace, call)
	if attempt.Receipt == nil || len(published.state.data.Publication.Files) != 1 {
		t.Fatal("retained failed-command receipt did not reach published projection")
	}
	receiptText := attempt.Receipt.Text
	liveDiffScopeCapture(t, store, workspace, "blocked-child", "child", filepath.Join(workspace, "child.go"), "old", "new")
	blocked, release := savedReviewLoaderBlock(t, store)
	savedReviewLoaderEnqueue(t, loader, savedReviewLoaderScope(workspace, false, "main", "blocked-child"))
	savedReviewLoaderEntered(t, blocked)
	if loader.state.data != data || data.Len() != 1 || !reflect.DeepEqual(data.Publication.Files, published.state.data.Publication.Files) {
		t.Fatal("in-flight read mutated published Diff data")
	}
	release()
	savedReviewLoaderAccept(t, loader)
	if data.Len() != 1 || !reflect.DeepEqual(data.Publication.Files, published.state.data.Publication.Files) {
		t.Fatal("completed read mutated an earlier publication")
	}
	if got := reviewSnapshotAttempt(t, loader.state.data, workspace, call).Receipt; got != attempt.Receipt || got.Text != receiptText {
		t.Fatal("scope expansion reloaded or changed immutable Activity receipt")
	}

	before := loader.state.data
	// The first retained call is valid, but the second is absent. No partial
	// attempt, receipt, order or byte accounting may escape this failed batch.
	liveDiffScopeCapture(t, store, workspace, "main", "late", filepath.Join(workspace, "late.go"), "a", "b")
	record, found, err := store.read(workspace, "late", false)
	if err != nil || !found {
		t.Fatalf("read retained fixture: found=%v error=%v", found, err)
	}
	savedReviewLoaderEnqueue(t, loader, sessionreview.Event{Kind: "change", Changes: []sessionreview.Change{{Workspace: workspace, Thread: "main", ID: record.History.ChangeID, Correlation: "late", Calls: []string{"late", "missing"}}}})
	result := savedReviewLoaderResult(t, loader)
	if accepted, err := loader.accept(result); accepted || err == nil {
		t.Fatalf("partial read accepted=%v error=%v", accepted, err)
	}
	if loader.state.data != before || before.Len() != 2 || reviewSnapshotAttempt(t, before, workspace, call).Receipt != attempt.Receipt {
		t.Fatal("failed read published partial state or replaced receipt")
	}
}

func TestSavedReviewLoaderRejectsStaleResults(t *testing.T) {
	for _, boundary := range []string{"resync", "attachment"} {
		t.Run(boundary, func(t *testing.T) {
			loader, store, workspace := savedReviewLoaderFixture(t)
			liveDiffScopeCapture(t, store, workspace, "main", "first", filepath.Join(workspace, "first.go"), "a", "b")
			savedReviewLoaderEnqueue(t, loader, savedReviewLoaderScope(workspace, true, "main"))
			stale := savedReviewLoaderResult(t, loader)
			if boundary == "attachment" {
				state := savedDiff{store: store}
				state.reset()
				loader.replace(t.Context(), state)
			}
			savedReviewLoaderEnqueue(t, loader, savedReviewLoaderScope(workspace, true, "main"))
			if accepted, err := loader.accept(stale); accepted || err != nil {
				t.Fatalf("stale publication accepted=%v error=%v", accepted, err)
			}
			if loader.state.data.Len() != 0 {
				t.Fatal("stale result changed current attachment")
			}
			if result := savedReviewLoaderAccept(t, loader); len(result.state.data.Publication.Files) != 1 {
				t.Fatal("current generation failed to recover retained evidence")
			}
		})
	}
}

func TestSavedReviewLoaderPhysicalCapacitySurvivesReplacement(t *testing.T) {
	loader, store, workspace := savedReviewLoaderFixture(t)
	liveDiffScopeCapture(t, store, workspace, "blocked-child", "edit", filepath.Join(workspace, "edit.go"), "a", "b")
	blocked, release := savedReviewLoaderBlock(t, store)
	savedReviewLoaderEnqueue(t, loader, savedReviewLoaderScope(workspace, true, "blocked-child"))
	savedReviewLoaderEntered(t, blocked)
	state := savedDiff{store: store}
	state.reset()
	loader.replace(t.Context(), state)
	// A second independent gate proves that the replacement worker also
	// reached storage, rather than merely occupying a queued logical job.
	secondStore := *store
	second, releaseSecond := savedReviewLoaderBlock(t, &secondStore)
	state.store = &secondStore
	loader.replace(t.Context(), state)
	savedReviewLoaderEnqueue(t, loader, savedReviewLoaderScope(workspace, true, "blocked-child"))
	savedReviewLoaderEntered(t, second)
	loader.replace(t.Context(), state)
	if err := loader.enqueue(savedReviewLoaderScope(workspace, true, "blocked-child")); err == nil || !strings.Contains(err.Error(), "capacity exhausted") {
		t.Fatalf("third physical read was not bounded: %v", err)
	}
	release()
	releaseSecond()
	// Wait for an actual physical slot, not an arbitrary scheduler delay.
	select {
	case loader.slots <- struct{}{}:
		<-loader.slots
	case <-time.After(5 * time.Second):
		t.Fatal("retired storage worker did not release physical capacity")
	}
	savedReviewLoaderEnqueue(t, loader, savedReviewLoaderScope(workspace, true, "blocked-child"))
	for {
		result := savedReviewLoaderResult(t, loader)
		accepted, err := loader.accept(result)
		if err != nil {
			t.Fatal(err)
		}
		if accepted {
			if len(result.state.data.Publication.Files) != 1 {
				t.Fatal("released capacity did not recover retained evidence")
			}
			break
		}
	}
}

func TestSavedReviewLoaderBacklogOverflowResyncsRetainedEvidence(t *testing.T) {
	loader, store, workspace := savedReviewLoaderFixture(t)
	blocked, release := savedReviewLoaderBlock(t, store)
	savedReviewLoaderEnqueue(t, loader, savedReviewLoaderScope(workspace, true, "blocked-child"))
	savedReviewLoaderEntered(t, blocked)
	for i := range 34 {
		call := fmt.Sprintf("edit-%02d", i)
		liveDiffScopeCapture(t, store, workspace, "blocked-child", call, filepath.Join(workspace, call+".go"), "before", "after")
		savedReviewLoaderEnqueue(t, loader, sessionreview.Event{Kind: "change", Changes: []sessionreview.Change{{Workspace: workspace, Thread: "blocked-child", Calls: []string{call}}}})
	}
	if !loader.resync || len(loader.pending) != 0 {
		t.Fatal("durable backlog overflow failed to request a bounded resync")
	}
	savedReviewLoaderEnqueue(t, loader, savedReviewLoaderScope(workspace, true, "blocked-child"))
	release()
	if accepted, err := loader.accept(savedReviewLoaderResult(t, loader)); accepted || err != nil {
		t.Fatalf("pre-overflow generation accepted=%v error=%v", accepted, err)
	}
	result := savedReviewLoaderAccept(t, loader)
	if loader.resync || len(result.state.data.Publication.Files) != 34 || loader.state.data.Len() != 34 {
		t.Fatalf("resync lost durable backlog: files=%d attempts=%d resync=%v", len(result.state.data.Publication.Files), loader.state.data.Len(), loader.resync)
	}
}

func TestSavedReviewLoaderOfflineHasNoWorker(t *testing.T) {
	loader := newSavedReviewLoader(context.Background(), savedDiff{})
	if loader != nil || loader.updates() != nil {
		t.Fatal("offline Diff allocated a storage worker or update channel")
	}
	loader.close()
	u, _ := newAppServerTestUI()
	u.ensureShell()
	u.shell.applyDiff(t.Context(), savedReviewLoaderScope("/workspace", true, "main"))
	if u.shell.diffFailure != "" || !u.shell.saved.scope.Workspaces["/workspace"]["main"] || u.shell.saved.data.Publication.Counts.Net != nil {
		t.Fatal("attachment without retained storage rejected scope or claimed known counts")
	}
	u.shell.applyDiff(t.Context(), sessionreview.Event{Kind: "change", Changes: []sessionreview.Change{{Workspace: "/workspace", Thread: "main", ID: "amber1", Calls: []string{"call"}}}})
	if u.shell.diffFailure == "" {
		t.Fatal("referencing change without storage did not report unavailable evidence")
	}
}
