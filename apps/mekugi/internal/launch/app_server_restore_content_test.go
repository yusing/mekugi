package launch

import (
	"bytes"

	"github.com/yusing/mekugi/backends/codex/binding"

	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"

	json "encoding/json/v2"
	"fmt"
	"path/filepath"
	"testing"
)

func restoreContentRequests(t *testing.T, w *appServerTestInput) []struct {
	Method string         `json:"method"`
	Params map[string]any `json:"params"`
} {
	t.Helper()
	var requests []struct {
		Method string         `json:"method"`
		Params map[string]any `json:"params"`
	}
	for line := range bytes.SplitSeq(bytes.TrimSpace(w.Bytes()), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var request struct {
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if err := json.Unmarshal(line, &request); err != nil {
			t.Fatal(err)
		}
		requests = append(requests, request)
	}
	return requests
}

func TestAppServerRestoreDiffScopeBeforeNewTurn(t *testing.T) {
	workspace, other := t.TempDir(), t.TempDir()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, edit := range []struct{ workspace, thread, call, path string }{
		{workspace, "main", "root-edit", filepath.Join(workspace, "root.go")},
		{workspace, "child", "child-edit", filepath.Join(workspace, "child.go")},
		{workspace, "unrelated", "other-thread", filepath.Join(workspace, "unrelated.go")},
		{other, "main", "other-workspace", filepath.Join(other, "foreign.go")},
	} {
		liveDiffScopeCapture(t, store, edit.workspace, edit.thread, edit.call, edit.path, "before", "after")
	}
	auto := newLivePreview(t.Context())
	defer auto.Close()
	auto.Enable()
	u := newAppServerSessionTestUI(t, workspace)
	u.review = testReview(auto)
	sub := testReview(auto).Subscribe()
	restoreDiffThread(&u.attached, u.shell, u.review, u.reviewSource, u.preparePane, binding.ThreadInfo{ID: "main", Cwd: workspace})
	u.review.IncludeThread(workspace, "child", false)
	var lastScope *previewmodel.Scope
	for len(sub.Events()) > 0 {
		event := <-sub.Events()
		if event.Kind == "scope" {
			lastScope = event.Scope
		}
	}
	if lastScope == nil || !lastScope.Workspaces[workspace]["main"] || !lastScope.Workspaces[workspace]["child"] || lastScope.Workspaces[workspace]["unrelated"] {
		t.Fatalf("native broker missed restored scope: %+v", lastScope)
	}
	reader := testReplayStore(mekugiReplayStore{directory: store.directory})
	files, err := reader.liveDiffSnapshotFiles(t.Context(), testReview(auto).Scope())
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("restored diff scope included unrelated edits or lost owned edits: %+v", files)
	}
	paths := map[string]bool{}
	for _, file := range files {
		paths[file.Path] = true
	}
	if !paths[filepath.Join(workspace, "root.go")] || !paths[filepath.Join(workspace, "child.go")] {
		t.Fatalf("root or child durable edit absent before provider turn: %+v", paths)
	}
}

func TestAppServerRestoreWaitsForDurableDiffBeforeInput(t *testing.T) {
	for _, viaPreviews := range []bool{false, true} {
		t.Run(fmt.Sprint(viaPreviews), func(t *testing.T) {
			workspace := t.TempDir()
			store, err := openMekugiReplayStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			liveDiffScopeCapture(t, store, workspace, "main", "edit", filepath.Join(workspace, "restored.go"), "before", "after")
			u := newAppServerSessionTestUI(t, workspace)
			w := u.Host.Input.(*appServerTestInput)
			auto := newLivePreview(t.Context())
			defer auto.Close()
			auto.Enable()
			u.review = testReview(auto)
			setSavedReviewStore(terminalTest(u), store)
			sub := testReview(auto).Subscribe()
			if err := u.restoreContent(binding.ThreadInfo{ID: "main", Cwd: workspace}); err != nil {
				t.Fatal(err)
			}

			u.Text = "next turn"
			appServerTestKeys(t, u, "\r")
			if u.restoring == nil || len(u.review.Projection.Data.Publication.Files) != 0 || len(restoreContentRequests(t, w)) != 0 {
				t.Fatal("input unlocked before the Diff controller consumed the restored scope")
			}
			if viaPreviews {
				for _, event := range testReview(auto).TakePreviews(sub) {
					applyReview(&u.attached, u.shell, u.review, u.reviewSource, u.preparePane, &u.pendingEditReceipts, u.clock, t.Context(), event)
				}
				if err := u.finishRestoredContent(); err != nil {
					t.Fatal(err)
				}
			} else {
				for len(sub.Events()) > 0 {
					applyReview(&u.attached, u.shell, u.review, u.reviewSource, u.preparePane, &u.pendingEditReceipts, u.clock, t.Context(), <-sub.Events())
					if err := u.finishRestoredContent(); err != nil {
						t.Fatal(err)
					}
				}
			}
			if u.restoring != nil || len(u.review.Projection.Data.Publication.Files) != 1 || u.review.Failure != "" {
				t.Fatalf("durable Diff not hydrated: %s", u.review.Failure)
			}
			appServerTestKeys(t, u, "\r")
			requests := restoreContentRequests(t, w)
			if len(requests) != 1 || requests[0].Method != "turn/start" {
				t.Fatal("input did not unlock after hydration")
			}

		})
	}
}
