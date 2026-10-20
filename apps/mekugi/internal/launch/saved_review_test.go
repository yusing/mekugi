package launch

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/yusing/mekugi/libraries/legacycall"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/diffmodel"

	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
)

// savedDiffWorkspace gives saved-pane fixtures a real project outside /tmp,
// whose captures are intentionally omitted from that pane.
func savedDiffWorkspace(t *testing.T) string {
	t.Helper()
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	workspace, err := os.MkdirTemp(cache, "mekugi-saved-diff-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(workspace); err != nil {
			t.Error(err)
		}
	})
	return workspace
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
	setSavedReviewStore(terminalTest(u), testReplayStore(mekugiReplayStore{directory: writer.directory}))
	review := u.review
	writer.liveDiff = review.Publish
	sub := review.Subscribe()
	apply := func() {
		t.Helper()
		for _, event := range review.TakePreviews(sub) {
			applyReview(&u.attached, u.shell, u.review, u.reviewSource, u.preparePane, &u.pendingEditReceipts, u.clock, t.Context(), event)
		}
		if u.review.Failure != "" {
			t.Fatal(u.review.Failure)
		}
	}
	paths := func() []string {
		var result []string
		for _, file := range u.review.Projection.Data.Publication.Files {
			result = append(result, filepath.Base(file.Path))
		}
		slices.Sort(result)
		return result
	}
	restoreDiffThread(&u.attached, u.shell, u.review, u.reviewSource, u.preparePane, binding.ThreadInfo{ID: "main", Cwd: workspace}, true)
	restoreDiffThread(&u.attached, u.shell, u.review, u.reviewSource, u.preparePane, binding.ThreadInfo{ID: "child", Cwd: workspace}, false)
	apply()
	if got := paths(); !slices.Equal(got, []string{"child.go", "root.go"}) {
		t.Fatalf("restored saved Diff: %v", got)
	}
	terminalTest(u).selectNativePane(1)
	if !terminalTest(u).DiffOpen {
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
	history := legacycall.History{ToolName: nativeExecCommandToolName, Root: workspace, ExecutingThread: "main", CorrelationID: call, ChangeID: id,
		ReviewFiles: retainedReviewFiles([]diffmodel.ReviewFile{diffmodel.RenderReviewFile(filepath.Join(workspace, "formatted.go"), filepath.Join(workspace, "formatted.go"), "old\n", "new\n")}),
		ExecOutcome: (*legacycall.Outcome)(&changecapture.Outcome{Status: changecapture.StatusFailed, Coverage: changecapture.CoverageExact, Exit: new(2)}),
	}
	if err := writer.put(t.Context(), workspace, map[string]legacycall.History{call: history}); err != nil {
		t.Fatal(err)
	}
	apply()
	if got := paths(); !slices.Equal(got, []string{"child.go", "formatted.go", "root.go"}) {
		t.Fatalf("published saved Diff: %v", got)
	}
	assertCapturedCommand(t, u.testActivity(u.shell.Transcript), "main", "format", "Edit", 2)
	foundReceipt := false
	for _, record := range u.attached.source.Entries {
		entry := record.Source
		if entry.Native != nil && entry.Native.Item == "format" {
			foundReceipt = entry.Native.CapturedEdit != nil
		}
	}
	if !foundReceipt {
		t.Fatal("retained change did not reach its Activity command")
	}

	resetDiffScope(&u.attached, u.shell, u.review, u.reviewSource, u.preparePane, u.ctx)
	select {
	case <-sub.Gap():
	default:
		t.Fatal("root switch retained old mailbox")
	}
	restoreDiffThread(&u.attached, u.shell, u.review, u.reviewSource, u.preparePane, binding.ThreadInfo{ID: "next", Cwd: workspace}, true)
	review.Observe(workspace, "old-child", "main", false)
	review.Observe(workspace, "new-child", "next", false)
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
