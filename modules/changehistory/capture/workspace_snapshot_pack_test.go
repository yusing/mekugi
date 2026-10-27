package capture

import (
	"bytes"
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWorkspaceRepackPreservesUnreferencedAndBorrowedObjects(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("Git unavailable")
	}
	newRepo := func() *workspaceSnapshotRepo {
		root := t.TempDir()
		r := &workspaceSnapshotRepo{git: git, worktree: root, gitDir: filepath.Join(root, "private.git"), index: filepath.Join(root, "index")}
		if _, err := r.run(t.Context(), nil, "init", "--quiet"); err != nil {
			t.Fatal(err)
		}
		return r
	}
	borrowed, repo := newRepo(), newRepo()
	borrowedID, err := borrowed.run(t.Context(), strings.NewReader("borrowed evidence"), "hash-object", "-w", "--stdin")
	if err != nil {
		t.Fatal(err)
	}
	borrowedSize := borrowed.storeBytes(t.Context())
	if err := os.WriteFile(filepath.Join(repo.gitDir, "objects", "info", "alternates"), []byte(filepath.Join(borrowed.gitDir, "objects")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	random := rand.New(rand.NewPCG(7, 11))
	base := make([]byte, 128<<10)
	for i := range base {
		base[i] = byte(random.Uint32())
	}
	objects := map[string][]byte{}
	for i := range 4 {
		data := bytes.Clone(base)
		data[i] = byte(i)
		id, err := repo.run(t.Context(), bytes.NewReader(data), "hash-object", "-w", "--stdin")
		if err != nil {
			t.Fatal(err)
		}
		objects[strings.TrimSpace(string(id))] = data
		treeText := []byte(fmt.Sprintf("100644 blob %s\tfile\n", strings.TrimSpace(string(id))))
		tree, err := repo.run(t.Context(), bytes.NewReader(treeText), "mktree")
		if err != nil {
			t.Fatal(err)
		}
		objects[strings.TrimSpace(string(tree))] = treeText
	}
	before := repo.storeBytes(t.Context())
	if err := repo.repack(t.Context()); err != nil {
		t.Fatal(err)
	}
	after := repo.storeBytes(t.Context())
	if after <= 0 || after >= before/2 {
		t.Fatalf("packing did not reduce fixture: %d -> %d", before, after)
	}
	t.Logf("private object bytes: %d -> %d", before, after)
	objects[strings.TrimSpace(string(borrowedID))] = []byte("borrowed evidence")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := repo.repack(ctx); err == nil {
		t.Fatal("canceled packing succeeded")
	}
	for id, want := range objects {
		got, err := repo.run(t.Context(), nil, "cat-file", "-p", id)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("object %s changed after packing/cancellation: %v", id, err)
		}
	}
	if got := borrowed.storeBytes(t.Context()); got != borrowedSize {
		t.Fatalf("borrowed store changed: %d -> %d", borrowedSize, got)
	}
}

func TestWorkspaceCanceledRepackKeepsSizeCheckDue(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("Git unavailable")
	}
	root := t.TempDir()
	repo := &workspaceSnapshotRepo{owner: &Workspaces{directory: t.TempDir()}, git: git, workspace: root, worktree: root, gitDir: filepath.Join(root, "private.git"), index: filepath.Join(root, "index"), lockPath: filepath.Join(root, "lock"), gate: make(chan struct{}, 1), pathspec: "."}
	if _, err := repo.run(t.Context(), nil, "init", "--quiet"); err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(t.TempDir(), "git")
	marker := filepath.Join(t.TempDir(), "packing")
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
	script := "#!/bin/sh\ncase \" $* \" in\n*' count-objects '*) printf 'size: 600000\\n'; exit 0;;\n*' repack '*) : > " + quote(marker) + "; exec sleep 10;;\nesac\nexec " + quote(git) + " \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	repo.git, repo.snapshots = wrapper, workspaceSnapshotStoreCheck
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := repo.snapshot(ctx); done <- err }()
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("snapshot stopped before packing: %v", err)
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	if err := <-done; err == nil || repo.snapshots < workspaceSnapshotStoreCheck {
		t.Fatalf("canceled maintenance was not left due: count=%d error=%v", repo.snapshots, err)
	}
	// An unsupported or failed retry must still enforce the existing size cap.
	script = strings.Replace(script, ": > "+quote(marker)+"; exec sleep 10", "exit 1", 1)
	if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(repo.gitDir, "oversized-state")
	if err := os.WriteFile(sentinel, nil, 0600); err != nil {
		t.Fatal(err)
	}
	_, _ = repo.snapshot(t.Context())
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("failed retry bypassed disk-cap fallback: %v", err)
	}
}
