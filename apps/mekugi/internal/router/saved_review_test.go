package router

import (
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"

	"github.com/yusing/mekugi/backends/codex/binding"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/diffmodel"

	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"

	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
)

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
	u.shell.saved.store = testReplayStore(mekugiReplayStore{directory: writer.directory})
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
		for _, file := range u.shell.saved.data.Publication.Files {
			result = append(result, filepath.Base(file.Path))
		}
		slices.Sort(result)
		return result
	}
	u.restoreDiffThread(binding.ThreadInfo{ID: "main", Cwd: workspace}, true)
	u.restoreDiffThread(binding.ThreadInfo{ID: "child", Cwd: workspace}, false)
	apply()
	if got := paths(); !slices.Equal(got, []string{"child.go", "root.go"}) {
		t.Fatalf("restored saved Diff: %v", got)
	}
	u.shell.selectNativePane(1)
	if !u.shell.DiffOpen {
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
		ExecOutcome: &changecapture.Outcome{Status: changecapture.StatusFailed, Coverage: changecapture.CoverageExact, Exit: new(2)},
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
	for _, record := range u.view.Entries {
		entry := record.Source
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
	u.restoreDiffThread(binding.ThreadInfo{ID: "next", Cwd: workspace}, true)
	observeReview(review, workspace, "old-child", codexresponses.TurnMetadata{RequestKind: "turn", ParentThreadID: "main", SubagentKind: "review"})
	observeReview(review, workspace, "new-child", codexresponses.TurnMetadata{RequestKind: "turn", ParentThreadID: "next", SubagentKind: "review"})
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
	review := sessionreview.New(t.Context(), previewmodel.New(changecapture.ReadFile), nil)
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
	case <-worker.Done():
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
