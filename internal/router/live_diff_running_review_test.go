package router

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExecRunningPreviewReportsOversizedFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "large")
	writeTestFile(t, path, strings.Repeat("old\n", 400000))
	before := snapshotExecFile(path, nil)
	writeTestFile(t, path, strings.Repeat("new\n", 400000))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	broker := newLiveDiffBroker(ctx)
	broker.setScope(liveDiffScope{Workspaces: map[string]map[string]bool{root: {"thread": true}}})
	go runExecScopePreview(ctx, broker, execObservation{Class: execScoped.String(), Files: []execFileSnapshot{before}}, liveDiffPreview{ID: "oversized", Workspace: root, Thread: "thread"})
	preview := waitExecScopePreview(t, broker, func(preview liveDiffPreview) bool { return preview.ID == "oversized" && len(preview.Files) == 1 })
	if !strings.Contains(preview.Files[0].Incomplete, "content bound") || strings.Contains(preview.Files[0].Diff, "+new") || strings.Contains(preview.Files[0].Diff, "-old") {
		t.Fatalf("oversized display hid bounds or guessed content: %+v", preview)
	}
	cancel()
	waitExecScopePreviewGone(t, broker, "oversized")
}

func TestExecRunningPreviewRetriesBudgetedFiles(t *testing.T) {
	root := t.TempDir()
	const rows = 56_000 // Each snapshot is over 600 KiB; two exceed the 1 MiB poll budget.
	baseline := strings.Repeat("stable row\n", rows) + "old ending\n"
	paths := []string{filepath.Join(root, "first.txt"), filepath.Join(root, "second.txt")}
	files := make([]execFileSnapshot, 0, len(paths))
	for _, path := range paths {
		if err := os.WriteFile(path, []byte(baseline), 0o600); err != nil {
			t.Fatal(err)
		}
		before := snapshotExecFile(path, nil)
		if before.Error != "" || len(before.Content) <= 600<<10 {
			t.Fatalf("fixture snapshot for %s did not exceed 600 KiB: size=%d err=%q", path, len(before.Content), before.Error)
		}
		files = append(files, before)
	}
	updated := strings.Repeat("stable row\n", rows) + "new ending\n"
	for _, path := range paths {
		if err := os.WriteFile(path, []byte(updated), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	broker := newLiveDiffBroker(ctx)
	broker.setScope(liveDiffScope{Workspaces: map[string]map[string]bool{root: {"thread": true}}})
	const previewID = "running:budgeted"
	go runExecScopePreview(ctx, broker, execObservation{Class: execScoped.String(), Files: files}, liveDiffPreview{
		ID: previewID, Workspace: root, Thread: "thread", Caller: "budget-test",
	})
	preview := waitExecScopePreview(t, broker, func(preview liveDiffPreview) bool {
		return preview.ID == previewID && preview.Status == "RUNNING · observed so far" && len(preview.Files) == 2
	})
	byPath := make(map[string]string, len(preview.Files))
	for _, file := range preview.Files {
		byPath[file.AfterPath] = file.Diff
	}
	for _, path := range paths {
		diff, ok := byPath[path]
		if !ok || !strings.Contains(diff, "-old ending") || !strings.Contains(diff, "+new ending") {
			t.Errorf("budgeted running preview did not retain the changed tail for %s: present=%v diff suffix=%q", path, ok, diff)
		}
	}
	cancel()
	waitExecScopePreviewGone(t, broker, previewID)
}
