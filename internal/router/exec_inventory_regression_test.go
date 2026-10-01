package router

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExecInventoryRegressionSameSizeFilter(t *testing.T) {
	w := t.TempDir()
	inventoryGit(t, w, "init", "--quiet")
	inventoryGit(t, w, "config", "filter.case.clean", "tr A-Z a-z")
	inventoryGit(t, w, "config", "filter.case.smudge", "tr a-z A-Z")
	writeTestFile(t, filepath.Join(w, ".gitattributes"), "file.txt filter=case\n")
	writeTestFile(t, filepath.Join(w, "file.txt"), "BEFORE\n")
	inventoryGit(t, w, "add", "-A")
	inventoryGit(t, w, "commit", "--quiet", "-m", "baseline")
	o := observeInventoryCommand(t, w, w, "make test", nil)
	writeTestFile(t, filepath.Join(w, "file.txt"), "AFTERS\n")
	r := inventoryReview(t, w, inventoryReviews(t, o, nil), "file.txt")
	if r.Incomplete != "" || !strings.Contains(r.Diff, "-BEFORE") || !strings.Contains(r.Diff, "+AFTERS") || strings.Contains(r.Diff, "-before") {
		t.Fatalf("fabricated baseline: %s", r.Diff)
	}
}
func TestExecInventoryRegressionLargeDirtyBaseline(t *testing.T) {
	w := t.TempDir()
	inventoryRepository(t, w, map[string]string{"tracked": "tracked\n"})
	writeTestFile(t, filepath.Join(w, "dirty.txt"), strings.Repeat("a", 5<<20))
	o := observeInventoryCommand(t, w, w, "make test", nil)
	if _, ok := o.Inventory.file(filepath.Join(w, "dirty.txt")); !ok {
		t.Fatal("5 MiB baseline dropped despite 8 MiB content and observation budgets")
	}
}
func TestExecInventoryRegressionPreservedMtime(t *testing.T) {
	w := t.TempDir()
	inventoryRepository(t, w, map[string]string{"file.txt": "before\n"})
	file := filepath.Join(w, "file.txt")
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	o := observeInventoryCommand(t, w, w, "make test", nil)
	writeTestFile(t, file, "afters\n")
	if err := os.Chtimes(file, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if reviews := inventoryReviews(t, o, nil); len(reviews) == 0 {
		t.Fatal("same-length rewrite with restored mtime disappeared")
	}
}
func TestExecInventoryRegressionReplacedDirectorySymlink(t *testing.T) {
	w, outside := t.TempDir(), t.TempDir()
	inventoryRepository(t, w, map[string]string{"dir/file.txt": "inside\n"})
	writeTestFile(t, filepath.Join(outside, "file.txt"), "OUTSIDE-CONTENT\n")
	o := observeInventoryCommand(t, w, w, "make test", nil)
	if err := os.Rename(filepath.Join(w, "dir"), filepath.Join(w, "saved")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(w, "dir")); err != nil {
		t.Fatal(err)
	}
	for _, r := range inventoryReviews(t, o, nil) {
		if strings.Contains(r.Diff, "OUTSIDE-CONTENT") {
			t.Fatalf("inventory followed symlink directory outside workspace: %s", r.Diff)
		}
	}
}
func TestExecInventoryRegressionLargeCleanBaseline(t *testing.T) {
	w := t.TempDir()
	inventoryRepository(t, w, map[string]string{"file.txt": strings.Repeat("a", 5<<20)})
	o := observeInventoryCommand(t, w, w, "make test", nil)
	writeTestFile(t, filepath.Join(w, "file.txt"), strings.Repeat("b", 5<<20))
	r := inventoryReview(t, w, inventoryReviews(t, o, nil), "file.txt")
	if r.Incomplete != "" {
		t.Fatalf("5 MiB file within per-side budget lost exact diff: %s", r.Incomplete)
	}
}
func TestExecInventoryRegressionResolvedNoGitRegression(t *testing.T) {
	w := t.TempDir()
	writeTestFile(t, filepath.Join(w, "file.txt"), "before\n")
	b := captureResolvedBaseline(w, "bash")
	writeTestFile(t, filepath.Join(w, "file.txt"), "after\n")
	if got := b.file(filepath.Join(w, "file.txt")); got.Error != "" || got.Content != "before\n" {
		t.Fatalf("previously exact dynamic patch baseline lost: %s", got.Error)
	}
}

func TestExecInventorySharedBlobBudget(t *testing.T) {
	w := t.TempDir()
	inventoryRepository(t, w, map[string]string{"a": "same", "b": "same"})
	inventory := captureExecInventory(w, time.Now().Add(time.Second), new(8))
	if len(inventory.Blobs) != 2 {
		t.Fatal("both byte-identical baselines should fit exactly")
	}
	budget := 4
	got := inventory.blobs(t.Context(), []string{"a", "b"}, &budget)
	if len(got) != 1 || got["a"].Content != "same" || budget != 0 {
		t.Fatalf("duplicate blob overspent budget: files=%d remaining=%d", len(got), budget)
	}
}

func TestExecInventoryTrimDoesNotMutateOriginal(t *testing.T) {
	original := execInventory{Files: []execFileSnapshot{{Path: "large", Content: "large"}, {Path: "small", Content: "s"}}}
	bounded := original
	boundExecInventory(&bounded, 0)
	if original.Files[0].Content != "large" || original.Files[1].Content != "s" {
		t.Fatal("trimming changed the original baseline")
	}
}

func TestExecInventoryLegacyBlobDoesNotBecomeVerifiedEvidence(t *testing.T) {
	w := t.TempDir()
	inventoryRepository(t, w, map[string]string{"file": "before"})
	inventory := captureExecInventory(w, time.Now().Add(time.Second), new(maxExecContentBytes))
	inventory.Version = 0
	writeTestFile(t, filepath.Join(w, "file"), "after")
	baseline := resolvedStockBaseline{Root: w, Inventory: inventory}
	if got := baseline.file(filepath.Join(w, "file")); got.Error == "" {
		t.Fatalf("legacy unverified blob became exact evidence: %+v", got)
	}
}

func TestExecInventoryInsideSymlinkAncestorIsNotFollowed(t *testing.T) {
	w := t.TempDir()
	inventoryRepository(t, w, map[string]string{"dir/file": "before", "target/file": "DO-NOT-FOLLOW"})
	observation := observeInventoryCommand(t, w, w, "make test", nil)
	if err := os.Rename(filepath.Join(w, "dir"), filepath.Join(w, "saved")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target", filepath.Join(w, "dir")); err != nil {
		t.Fatal(err)
	}
	reviews := inventoryReviews(t, observation, nil)
	if got := inventoryReview(t, w, reviews, "dir/file"); got.Incomplete == "" || strings.Contains(got.Diff, "DO-NOT-FOLLOW") {
		t.Fatalf("followed in-workspace ancestor symlink: %+v", got)
	}
}
