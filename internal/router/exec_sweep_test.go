package router

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// sweepTestStart skips only filesystems for which the sweep has no comparable clock.
func sweepTestStart(t *testing.T, root string) time.Time {
	t.Helper()
	if execRemoteFilesystem(root) {
		t.Skip("sweep unavailable on network or FUSE filesystem")
	}
	start := execWindowStart(root)
	if start.IsZero() {
		t.Skip("filesystem change clock unavailable")
	}
	return start
}

func sweepTestFindings(result execSweepResult) map[string]execSweepFinding {
	findings := make(map[string]execSweepFinding, len(result.Findings))
	for _, finding := range result.Findings {
		findings[finding.Path] = finding
	}
	return findings
}

func TestExecSweepFindsChangesAndDeletionParent(t *testing.T) {
	root := t.TempDir()
	modified := filepath.Join(root, "modified.txt")
	deleted := filepath.Join(root, "deleted.txt")
	newDir := filepath.Join(root, "new")
	newFile := filepath.Join(newDir, "file.txt")
	for _, path := range []string{modified, deleted} {
		if err := os.WriteFile(path, []byte("before"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	start := sweepTestStart(t, root)
	if err := os.WriteFile(modified, []byte("after"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(deleted); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(newDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newFile, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := sweepExecRoot(root, start, nil)
	if result.Unswept != "" {
		t.Fatalf("unexpected unswept reason: %s", result.Unswept)
	}
	findings := sweepTestFindings(result)
	for _, path := range []string{modified, newDir, newFile} {
		if _, ok := findings[path]; !ok {
			t.Errorf("missing changed path %q: %+v", path, result.Findings)
		}
	}
	if finding := findings[newDir]; !finding.Dir {
		t.Errorf("new directory not marked as directory: %+v", finding)
	}
	if _, ok := findings[deleted]; ok {
		t.Errorf("deleted path cannot be found by a post-call sweep: %+v", result.Findings)
	}
	if _, ok := findings[root]; !ok {
		t.Errorf("root entry change did not reveal deletion: %+v", result.Findings)
	}
	if finding := findings[newFile]; finding.Born && !finding.New {
		t.Errorf("new inode born after window start was not classified new: %+v", finding)
	}
}

func TestExecWindowStartExcludesEarlierClockTick(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "unchanged")
	writeTestFile(t, path, "old")
	before, _, _, ok := execFileTimes(path)
	if !ok {
		t.Skip("file clock unavailable")
	}
	start := execWindowStart(t.TempDir())
	if start.IsZero() {
		t.Skip("file clock cannot advance within capture budget")
	}
	if !start.After(before) {
		t.Fatalf("window %v includes unchanged inode at %v", start, before)
	}
	result := sweepExecRoot(root, start, nil)
	if len(result.Findings) != 0 || result.Unswept != "" {
		t.Fatalf("unchanged tree: %+v", result)
	}
}

func TestExecSweepIgnorePruningAndQuietParent(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{".git", ".hg", ".svn", ".jj", "node_modules", "__pycache__", "tagged", "venv", "ignored", "kept"} {
		if err := os.Mkdir(filepath.Join(root, path), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	start := sweepTestStart(t, root)
	for _, path := range []string{".git/a", ".hg/a", ".svn/a", ".jj/a", "node_modules/a", "__pycache__/a", "tagged/CACHEDIR.TAG", "tagged/a", "venv/pyvenv.cfg", "venv/a", "ignored/a.tmp", "kept/a.txt"} {
		writeTestFile(t, filepath.Join(root, path), "changed")
	}
	writeTestFile(t, filepath.Join(root, ".gitignore"), "ignored/\n*.tmp\n!kept/a.txt\n")
	result := sweepExecRoot(root, start, nil)
	if result.Unswept != "" {
		t.Fatalf("unexpected unswept reason: %s", result.Unswept)
	}
	findings := sweepTestFindings(result)
	if _, ok := findings[filepath.Join(root, "kept", "a.txt")]; !ok {
		t.Errorf("nonignored file missing: %+v", result.Findings)
	}
	for _, path := range []string{".git", ".hg", ".svn", ".jj", "ignored"} {
		if _, ok := findings[filepath.Join(root, path)]; ok {
			t.Errorf("pruned directory %q leaked into findings", path)
		}
	}
	if !result.Quiet[root] {
		t.Errorf("root should be quiet for changed, pruned child entries: %+v", result.Quiet)
	}
	// Built-in pruning applies only when no ignore file governs this directory.
	noIgnore := t.TempDir()
	if err := os.Mkdir(filepath.Join(noIgnore, "node_modules"), 0o700); err != nil {
		t.Fatal(err)
	}
	start = sweepTestStart(t, noIgnore)
	writeTestFile(t, filepath.Join(noIgnore, "node_modules", "a"), "new")
	result = sweepExecRoot(noIgnore, start, nil)
	if _, ok := sweepTestFindings(result)[filepath.Join(noIgnore, "node_modules")]; ok {
		t.Errorf("built-in pruned directory leaked: %+v", result.Findings)
	}
	if !result.Quiet[noIgnore] {
		t.Errorf("built-in pruned directory should quiet its parent: %+v", result.Quiet)
	}
}

func TestExecSweepSkipAndUnavailableRoot(t *testing.T) {
	root := t.TempDir()
	store := filepath.Join(root, "store")
	if err := os.Mkdir(store, 0o700); err != nil {
		t.Fatal(err)
	}
	start := sweepTestStart(t, root)
	writeTestFile(t, filepath.Join(store, "data"), "new")
	result := sweepExecRoot(root, start, []string{store})
	if result.Unswept != "" || !result.Quiet[root] {
		t.Fatalf("skip should be quiet without truncating sweep: %+v", result)
	}
	if slices.ContainsFunc(result.Findings, func(f execSweepFinding) bool { return strings.HasPrefix(f.Path, store) }) {
		t.Errorf("skipped subtree leaked: %+v", result.Findings)
	}
	if got := sweepExecRoot(root, time.Time{}, nil).Unswept; got == "" {
		t.Error("missing clock must report unswept")
	}
	if got := sweepExecRoot(filepath.Join(root, "missing"), start, nil).Unswept; got == "" {
		t.Error("unreadable root must report unswept")
	}
}
