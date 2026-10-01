package router

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi"
	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func inventoryInstallFixture(t *testing.T) (string, *execObservation) {
	t.Helper()
	w := t.TempDir()
	inventoryRepository(t, w, map[string]string{".gitignore": "FIXME.md\nnode_modules/\n", "package.json": "{}\n"})
	writeTestFile(t, filepath.Join(w, "FIXME.md"), "before\n")
	writeTestFile(t, filepath.Join(w, "node_modules", "pkg", "index.js"), "private dependency before\n")
	return w, observeInventoryCommand(t, w, w, "npm install", nil)
}

func TestExecInventoryDependencyDirectoryPersistsWithoutDescendants(t *testing.T) {
	w, observation := inventoryInstallFixture(t)
	writeTestFile(t, filepath.Join(w, "FIXME.md"), "after\n")
	writeTestFile(t, filepath.Join(w, "node_modules", "pkg", "index.js"), "private dependency after\n")
	reviews := inventoryReviews(t, observation, nil)
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := putTestChange(t, t.Context(), store, w, "install", reviews...)
	reopened, err := openMekugiReplayStore(store.directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, view := range []string{"", "summary", "history"} {
		output, err := reopened.readChanges(t.Context(), changeReadOptions{workspace: w, ids: []string{id}, view: view})
		if err != nil || !strings.Contains(output, "M node_modules/\n") || !strings.Contains(output, "FIXME.md") || strings.Contains(output, "index.js") || strings.Contains(output, "private dependency") {
			t.Fatalf("%s: %q, %v", view, output, err)
		}
	}
	directory := inventoryReview(t, w, reviews, "node_modules")
	if _, err := directory.Merge("", true, "revert"); err == nil {
		t.Fatal("metadata-only directory was replayable")
	}
	var composition mekugi.ReviewComposition
	if err := composition.ApplyWithHighlight(directory, false, false); err == nil {
		t.Fatal("metadata-only directory was composable")
	}
	// Stored snapshots must not hide a descendant listing or dependency content.
	raw := string(mustMarshalJSON(observation.Inventory))
	if strings.Contains(raw, "index.js") || strings.Contains(raw, "private dependency") {
		t.Fatalf("inventory retained dependency detail: %s", raw)
	}
}

func TestExecInventoryDependencyDirectoryNoopAndDeletion(t *testing.T) {
	w, observation := inventoryInstallFixture(t)
	if got := inventoryReviews(t, observation, nil); len(got) != 0 {
		t.Fatalf("no-op install invented changes: %+v", got)
	}
	// Remove precisely the temporary fixture tree; no workspace data is affected.
	if err := os.RemoveAll(filepath.Join(w, "node_modules")); err != nil {
		t.Fatal(err)
	}
	reviews := inventoryReviews(t, observation, nil)
	directory := inventoryReview(t, w, reviews, "node_modules")
	if !directory.Directory || directory.AfterPath != "" {
		t.Fatalf("missing directory deletion: %+v", directory)
	}
}

func TestExecInventoryOfflineNPMInstall(t *testing.T) {
	npm, err := exec.LookPath("npm")
	if err != nil {
		t.Skip("npm unavailable")
	}
	w := t.TempDir()
	writeTestFile(t, filepath.Join(w, "package.json"), `{"name":"capture-fixture","version":"1.0.0","dependencies":{"local-fixture":"file:./fixture"}}`)
	writeTestFile(t, filepath.Join(w, "fixture", "package.json"), `{"name":"local-fixture","version":"1.0.0"}`)
	writeTestFile(t, filepath.Join(w, "fixture", "index.js"), "module.exports = 1;\n")
	observation := observeInventoryCommand(t, w, w, "npm install --offline --ignore-scripts --no-audit --no-fund", nil)
	cmd := exec.Command(npm, "install", "--offline", "--ignore-scripts", "--no-audit", "--no-fund", "--cache", t.TempDir())
	cmd.Dir = w
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("offline npm install: %v: %s", err, output)
	}
	reviews := inventoryReviews(t, observation, nil)
	directory := inventoryReview(t, w, reviews, "node_modules")
	if !directory.Directory || directory.BeforePath != "" || directory.Diff != "" {
		t.Fatalf("install directory not collapsed: %+v", directory)
	}
	for _, review := range reviews {
		if strings.Contains(review.BeforePath, "node_modules/") || strings.Contains(review.AfterPath, "node_modules/") {
			t.Fatalf("dependency descendant escaped: %+v", review)
		}
	}
}

func TestUISnapshotMChangesDependencyDirectory(t *testing.T) {
	w, observation := inventoryInstallFixture(t)
	writeTestFile(t, filepath.Join(w, "FIXME.md"), "after\n")
	writeTestFile(t, filepath.Join(w, "node_modules", "pkg", "index.js"), "private dependency after\n")
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := putTestChange(t, t.Context(), store, w, "install", inventoryReviews(t, observation, nil)...)
	output, err := store.readChanges(t.Context(), changeReadOptions{workspace: w, ids: []string{id}, view: "summary"})
	if err != nil {
		t.Fatal(err)
	}
	u := mchangesDisplay(t, "mchanges --summary", output)
	u.view.painter.Theme = livediff.DarkTheme
	uisnapshot.Assert(t, "testdata/snapshots/mchanges-dependency-directory.txt", mainFeed(u, 100)+"\n")
}

func TestUISnapshotDependencyDirectoryReceipt(t *testing.T) {
	w, observation := inventoryInstallFixture(t)
	writeTestFile(t, filepath.Join(w, "node_modules", "pkg", "index.js"), "private dependency after\n")
	history := mekugiHistory{ReviewFiles: inventoryReviews(t, observation, nil)}
	v := newLiveActivityView()
	v.conversation = true
	v.painter.Theme = livediff.DarkTheme
	v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: 1, Agent: "Main", Kind: "tool", Text: editReceiptText(w, history), Observed: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}}})
	uisnapshot.Assert(t, "testdata/snapshots/dependency-directory-receipt.txt", strings.Join(v.renderFeed(100, 20).lines, "\n")+"\n")
}

func TestExecInventoryDirectoryBoundDoesNotInventChanges(t *testing.T) {
	w := t.TempDir()
	writeTestFile(t, filepath.Join(w, "node_modules", "pkg", "file"), "content")
	before := captureExecDirectory(w, "node_modules", time.Now().Add(-time.Second))
	after := captureExecDirectory(w, "node_modules", time.Now().Add(-time.Second))
	if before.Complete || after.Complete {
		t.Fatal("expired metadata capture reported complete")
	}
	review, found := execDirectoryReview(filepath.Join(w, "node_modules"), before, after)
	if !found || review.Directory || review.Incomplete == "" {
		t.Fatalf("bound became a modification or no-op: %+v", review)
	}
}
