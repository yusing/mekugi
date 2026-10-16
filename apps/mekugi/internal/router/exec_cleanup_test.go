package router

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"

	"github.com/yusing/mekugi/libraries/commandanalysis/execscope"
	"github.com/yusing/mekugi/libraries/diffmodel"
	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
)

func TestExecScopePreviewPublishesOnlyReviewChanges(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "tracked.txt")
		if err := os.WriteFile(path, []byte("original\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		before := changecapture.ReadFile(path, nil)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		broker := newLivePreview(ctx)
		setLiveScope(broker, previewmodel.Scope{Workspaces: map[string]map[string]bool{root: {"thread": true}}})
		sub := testReview(broker).Subscribe()
		testReview(broker).TakePreviews(sub) // Discard initial scope synchronization.
		const previewID = "running:cleanup-test"
		go runExecScopePreview(ctx, broker, changecapture.Observation{Class: execscope.Scoped.String(), Files: []changecapture.File{before}}, previewmodel.Preview{
			ID: previewID, Workspace: root, Thread: "thread",
		}, nil, nil)
		synctest.Wait()
		assertNoExecCleanupPreview(t, testReview(broker).TakePreviews(sub), "unchanged scope should stay out of the pane")

		advanceExecCleanupPreviewTicker(t)
		assertNoExecCleanupPreview(t, testReview(broker).TakePreviews(sub), "unchanged scope should stay out of the pane after polling")
		advanceExecCleanupPreviewTicker(t)
		assertNoExecCleanupPreview(t, testReview(broker).TakePreviews(sub), "unchanged review map was republished")

		if err := os.WriteFile(path, []byte("changed\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		advanceExecCleanupPreviewTicker(t)
		events := testReview(broker).TakePreviews(sub)
		changed := assertExecCleanupPreview(t, events, previewID, previewmodel.PreviewRunning)
		if len(changed.Files) != 1 || changed.Files[0].BeforePath != path || changed.Files[0].AfterPath != path {
			t.Fatalf("changed review files = %+v", changed.Files)
		}
		advanceExecCleanupPreviewTicker(t)
		assertNoExecCleanupPreview(t, testReview(broker).TakePreviews(sub), "unchanged changed-review map was republished")

		if err := os.WriteFile(path, []byte("original\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		advanceExecCleanupPreviewTicker(t)
		assertExecCleanupPreview(t, testReview(broker).TakePreviews(sub), previewID, "")

		cancel()
		synctest.Wait()
		assertNoExecCleanupPreview(t, testReview(broker).TakePreviews(sub), "cancellation republished an already removed preview")
		_, active := currentLivePreview(broker, previewID)
		if active {
			t.Errorf("preview %q remained active after cancellation", previewID)
		}
	})
}

func TestExecScopePreviewRetriesAfterBrokerAdmissionRejection(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "tracked.txt")
		if err := os.WriteFile(path, []byte("original\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		before := changecapture.ReadFile(path, nil)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		broker := newLivePreview(ctx)
		setLiveScope(broker, previewmodel.Scope{Workspaces: map[string]map[string]bool{root: {"thread": true}}})
		for index := range 16 {
			broker.Update(previewmodel.Preview{
				ID: filepath.Join("occupied", string(rune('a'+index))), Workspace: root, Thread: "thread", Status: "RUNNING · occupied",
				Files: []diffmodel.ReviewFile{diffmodel.RenderReviewPreviewFile("", path, "", "occupied\n")},
			}, false)
		}
		occupiedCount := len(broker.Current())
		if occupiedCount != 16 {
			t.Fatalf("occupied preview count = %d, want 16", occupiedCount)
		}
		sub := testReview(broker).Subscribe()
		seed := testReview(broker).TakePreviews(sub)
		seeded := 0
		for _, event := range seed {
			if event.Preview != nil {
				seeded++
			}
		}
		if seeded != 16 {
			t.Fatalf("subscriber seed contained %d active previews, want 16", seeded)
		}

		const previewID = "running:admission-retry"
		go runExecScopePreview(ctx, broker, changecapture.Observation{Class: execscope.Scoped.String(), Files: []changecapture.File{before}}, previewmodel.Preview{
			ID: previewID, Workspace: root, Thread: "thread",
		}, nil, nil)
		synctest.Wait()
		assertNoExecCleanupPreview(t, testReview(broker).TakePreviews(sub), "unchanged scope should not seek broker admission")
		assertExecCleanupPreviewNotActive(t, broker, previewID)

		if err := os.WriteFile(path, []byte("changed\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		advanceExecCleanupPreviewTicker(t)
		assertNoExecCleanupPreview(t, testReview(broker).TakePreviews(sub), "broker-full changed preview should be rejected")
		assertExecCleanupPreviewNotActive(t, broker, previewID)

		broker.Update(previewmodel.Preview{ID: "occupied/a"}, true)
		assertExecCleanupPreview(t, testReview(broker).TakePreviews(sub), "occupied/a", "")
		advanceExecCleanupPreviewTicker(t) // The file is unchanged since the rejected preview; retry it.
		preview := assertExecCleanupPreview(t, testReview(broker).TakePreviews(sub), previewID, previewmodel.PreviewRunning)
		if len(preview.Files) != 1 {
			t.Fatalf("retried preview lost its changed-file projection: %+v", preview)
		}
		active, found := currentLivePreview(broker, previewID)
		count := len(broker.Current())
		if !found || active.Status != previewmodel.PreviewRunning || count != 16 {
			t.Fatalf("retried preview not admitted to full broker: found=%v active=%+v count=%d", found, active, count)
		}

		cancel()
		synctest.Wait()
		assertExecCleanupPreview(t, testReview(broker).TakePreviews(sub), previewID, "")
	})
}

func TestExecScopePreviewDoesNotTreatReadBudgetAsChange(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "large.txt")
		original := strings.Repeat("old\n", 400000)
		if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
			t.Fatal(err)
		}
		before := changecapture.ReadFile(path, nil)
		if before.Error != "" || before.WatchStamp == "" {
			t.Fatalf("capture did not retain a preview metadata baseline: %+v", before)
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		broker := newLivePreview(ctx)
		setLiveScope(broker, previewmodel.Scope{Workspaces: map[string]map[string]bool{root: {"thread": true}}})
		sub := testReview(broker).Subscribe()
		testReview(broker).TakePreviews(sub)
		const previewID = "running:large-unchanged"
		go runExecScopePreview(ctx, broker, changecapture.Observation{Class: execscope.Scoped.String(), Files: []changecapture.File{before}}, previewmodel.Preview{
			ID: previewID, Workspace: root, Thread: "thread",
		}, nil, nil)
		advanceExecCleanupPreviewTicker(t)
		assertNoExecCleanupPreview(t, testReview(broker).TakePreviews(sub), "unchanged large file became a running change")
		_, active := currentLivePreview(broker, previewID)
		if active {
			t.Fatal("unchanged large file occupied a preview slot")
		}

		if err := os.WriteFile(path, []byte(strings.Repeat("new\n", 400000)), 0o600); err != nil {
			t.Fatal(err)
		}
		// The running preview polls a metadata stamp before attempting a
		// bounded read. A same-size rewrite can retain the same filesystem
		// timestamp when the test completes within one clock tick, so make the
		// fixture's change observable independently of timestamp resolution.
		changedTime := time.Now().Add(time.Hour)
		if err := os.Chtimes(path, changedTime, changedTime); err != nil {
			t.Fatal(err)
		}
		advanceExecCleanupPreviewTicker(t)
		changed := assertExecCleanupPreview(t, testReview(broker).TakePreviews(sub), previewID, "")
		if len(changed.Files) != 0 {
			t.Fatalf("unavailable large content opened a preview: %+v", changed)
		}
		cancel()
		synctest.Wait()
		assertNoExecCleanupPreview(t, testReview(broker).TakePreviews(sub), "cancellation republished a hidden preview")
	})
}

func assertExecCleanupPreviewNotActive(t *testing.T, broker *previewmodel.Session, id string) {
	t.Helper()
	_, active := currentLivePreview(broker, id)
	count := len(broker.Current())
	if active || count != 16 {
		t.Fatalf("rejected preview is active or changed the full broker: active=%v count=%d", active, count)
	}
}

func advanceExecCleanupPreviewTicker(t *testing.T) {
	t.Helper()
	time.Sleep(500 * time.Millisecond)
	synctest.Wait()
}

func assertExecCleanupPreview(t *testing.T, events []reviewEvent, id, status string) previewmodel.Preview {
	t.Helper()
	var previews []previewmodel.Preview
	for _, event := range events {
		if event.Preview != nil {
			previews = append(previews, *event.Preview)
		}
	}
	if len(previews) != 1 {
		t.Fatalf("got %d preview events, want one: %+v", len(previews), events)
	}
	preview := previews[0]
	if preview.ID != id || preview.Status != status {
		t.Fatalf("preview = %+v, want ID %q status %q", preview, id, status)
	}
	return preview
}

func assertNoExecCleanupPreview(t *testing.T, events []reviewEvent, message string) {
	t.Helper()
	for _, event := range events {
		if event.Preview != nil {
			t.Fatalf("%s: %+v", message, *event.Preview)
		}
	}
}
