package router

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi"
)

func TestMChangesWorkspaceAllocationIgnoresForeignIndexPayload(t *testing.T) {
	t.Parallel()
	for _, namespace := range []string{"foreign", ""} {
		t.Run("namespace="+namespace, func(t *testing.T) {
			store, err := openMekugiReplayStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			workspace := t.TempDir()
			// Valid identity, corrupt typed payload. Matching corruption must
			// still fail, but an unrelated session cannot block publication.
			data := fmt.Sprintf(`{"Version":1,"Workspace":%q,"Namespace":%q,"Streams":[{"Thread":"owner","Next":"broken"}],"Changes":{}}`, workspace, namespace)
			path := filepath.Join(store.directory, changeIndexName(workspace, namespace))
			if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
			id, err := store.reserveChange(t.Context(), t.TempDir(), "current", "capture")
			if namespace == "" {
				if err == nil || id != "" {
					t.Fatalf("matching corrupt index accepted: id=%q err=%v", id, err)
				}
			} else if err != nil || id != "amber1" {
				t.Fatalf("foreign corrupt payload blocked capture: id=%q err=%v", id, err)
			}
		})
	}
}

// An ID is a namespace-wide handle, not the position of a stream in one
// workspace's index. The same thread can edit two workspaces.
func TestMChangesWorkspaceStreamIDsSurviveRestartAndBranch(t *testing.T) {
	t.Parallel()
	f := newMChangesSliceFixture(t, "workspace-streams")
	other := t.TempDir()
	first := f.reserve(t, f.thread, "first")
	second, err := f.store.reserveChange(f.ctx, other, f.thread, "second")
	if err != nil {
		t.Fatal(err)
	}
	if first != "amber1" || second != "apple1" {
		t.Fatalf("workspace IDs = %q, %q; want amber1, apple1", first, second)
	}

	reopened, err := openMekugiReplayStore(f.store.directory)
	if err != nil {
		t.Fatal(err)
	}
	resume, release, err := reopened.beginSession(t.Context(), f.thread, "resumed")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	third, err := reopened.reserveChange(resume, f.workspace, "other-agent", "third")
	if err != nil {
		t.Fatal(err)
	}
	if third != "arch1" {
		t.Fatalf("resumed namespace reused stream: %q", third)
	}

	forkThread := uniqueTestThread("workspace-stream-fork")
	forkCtx, releaseFork, err := reopened.beginSession(t.Context(), forkThread, "forked")
	if err != nil {
		t.Fatal(err)
	}
	defer releaseFork()
	forkCtx = bindTestHandleScope(t, reopened, forkCtx, "", f.thread)
	forkID, err := reopened.reserveChange(forkCtx, filepath.Join(t.TempDir(), "fork"), forkThread, "fork-edit")
	if err != nil {
		t.Fatal(err)
	}
	if forkID != "ash1" {
		t.Fatalf("fork reused an inherited stream: %q", forkID)
	}
}

func TestMChangesLegacyPositionalStreamsSeedNewWorkspaceID(t *testing.T) {
	t.Parallel()
	f := newMChangesSliceFixture(t, "legacy-workspace-streams")
	for i, thread := range []string{f.thread, "legacy-agent-one", "legacy-agent-two"} {
		id, err := f.store.reserveChange(f.ctx, f.workspace, thread, "legacy-"+thread)
		if err != nil {
			t.Fatal(err)
		}
		if want := changeHandle(changeStreamName(i), 1); id != want {
			t.Fatalf("legacy seed stream %d = %q, want %q", i, id, want)
		}
	}
	store := f.store.scoped(f.ctx)
	if err := store.locked(f.ctx, func() error {
		index, err := store.readChangeIndex(f.workspace)
		if err != nil {
			return err
		}
		for i := range index.Streams {
			index.Streams[i].Name = ""
		}
		if err := store.writeChangeIndex(index); err != nil {
			return err
		}
		scope, _, err := store.readHandleScope(store.handleNamespace())
		if err != nil {
			return err
		}
		scope.NextChangeStream = 0
		return store.writeHandleScope(scope)
	}); err != nil {
		t.Fatal(err)
	}
	newID, err := f.store.reserveChange(f.ctx, t.TempDir(), f.thread, "new-workspace")
	if err != nil {
		t.Fatal(err)
	}
	if newID != "ash1" {
		t.Fatalf("new workspace reused legacy ID: %q, want ash1", newID)
	}
}

func TestMChangesWrongWorkspaceReadKeepsValidTargets(t *testing.T) {
	t.Parallel()
	f := newMChangesSliceFixture(t, "workspace-partial")
	other := t.TempDir()
	valid := f.reserve(t, f.thread, "valid")
	f.publish(t, valid, "valid", "valid-call", mekugiHistory{
		ReviewFiles: []mekugi.ReviewFile{mekugi.RenderReviewFile("file.txt", "file.txt", "old\n", "new\n")},
	})
	foreign, err := f.store.reserveChange(f.ctx, other, f.thread, "foreign")
	if err != nil {
		t.Fatal(err)
	}
	for _, view := range []string{"", "--summary", "--history", "--list", "--net"} {
		command := "mchanges " + view + " " + valid + " " + foreign
		stdout, stderr, status := f.run(t, command)
		marker := valid
		if view == "--net" || view == "--summary" {
			marker = "file.txt"
		}
		if status == 0 || !strings.Contains(stdout, marker) || !strings.Contains(stderr, foreign) {
			t.Errorf("%q lost partial result or error: stdout=%q stderr=%q status=%d", command, stdout, stderr, status)
		}
	}
}

func TestMChangesWrongWorkspaceMutationIsAtomic(t *testing.T) {
	t.Parallel()
	f := newMChangesSliceFixture(t, "workspace-mutation")
	other := t.TempDir()
	valid := f.reserve(t, f.thread, "valid")
	f.publish(t, valid, "valid", "valid-call", mekugiHistory{
		ReviewFiles: []mekugi.ReviewFile{mekugi.RenderReviewFile("file.txt", "file.txt", "old\n", "new\n")},
	})
	foreign, err := f.store.reserveChange(f.ctx, other, f.thread, "foreign")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(f.workspace, "file.txt")
	writeTestFile(t, path, "new\n")
	for _, operation := range []string{"revert", "apply"} {
		options, err := parseChangeRead([]string{operation, valid, foreign}, f.workspace)
		if err != nil {
			t.Fatal(err)
		}
		output, status, err := f.store.mutateChanges(f.ctx, options)
		if err == nil || status == 0 || !strings.Contains(err.Error(), foreign) || readTestFile(t, path) != "new\n" {
			t.Errorf("%s mutated a partial selection: output=%q status=%d err=%v", operation, output, status, err)
		}
	}
}
