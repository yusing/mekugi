package router

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/yusing/mekugi/internal/ui/diffview"
)

func TestExecRunningPreviewOmitsOversizedFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, "large")
	writeTestFile(t, path, strings.Repeat("old\n", 400000))
	// Same-size writes can share a filesystem timestamp on fast runs. Make
	// the changed metadata explicit; this test covers the content-size bound.
	baselineTime := time.Unix(1, 0)
	if err := os.Chtimes(path, baselineTime, baselineTime); err != nil {
		t.Fatal(err)
	}
	before := snapshotExecFile(path, nil)
	writeTestFile(t, path, strings.Repeat("new\n", 400000))
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		broker := newLiveDiffBroker(ctx)
		broker.setScope(liveDiffScope{Workspaces: map[string]map[string]bool{root: {"thread": true}}})
		go runExecScopePreview(ctx, broker, execObservation{Class: execScoped.String(), Files: []execFileSnapshot{before}}, diffview.Preview{ID: "oversized", Workspace: root, Thread: "thread"}, nil)
		time.Sleep(time.Second)
		synctest.Wait()
		if len(broker.previews) != 0 {
			t.Fatalf("unavailable content opened a live card: %+v", broker.previews)
		}
		cancel()
	})
}

func TestLiveDiffSocketAndNewRegularFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, "socket")
	before := snapshotExecFile(path, nil)
	regular := filepath.Join(root, "file")
	if err := os.Mkdir(regular, 0o700); err != nil {
		t.Fatal(err)
	}
	beforeRegular := snapshotExecFile(regular, nil)
	if err := os.Remove(regular); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, regular, "new source\n")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		broker := newLiveDiffBroker(ctx)
		broker.setScope(liveDiffScope{Workspaces: map[string]map[string]bool{root: {"thread": true}}})
		go runExecScopePreview(ctx, broker, execObservation{Files: []execFileSnapshot{before, beforeRegular}}, diffview.Preview{ID: "socket", Workspace: root, Thread: "thread"}, nil)
		time.Sleep(time.Second)
		synctest.Wait()
		preview := broker.previews["socket"]
		if len(preview.Files) != 1 || preview.Files[0].AfterPath != regular || !strings.Contains(preview.Files[0].Diff, "+new source") {
			t.Fatalf("socket displayed or new regular file hidden: %+v", preview)
		}
	})
}

func TestExecRunningPreviewRetriesBudgetedFiles(t *testing.T) {
	t.Parallel()
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
	go runExecScopePreview(ctx, broker, execObservation{Class: execScoped.String(), Files: files}, diffview.Preview{
		ID: previewID, Workspace: root, Thread: "thread", Caller: "budget-test",
	}, nil)
	preview := waitExecScopePreview(t, broker, func(preview diffview.Preview) bool {
		return preview.ID == previewID && preview.Status == diffview.PreviewRunning && len(preview.Files) == 2
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
