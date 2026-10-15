package router

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/libraries/legacycall"

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
	if err := store.locked(f.ctx, func(store *mekugiReplayStore) error {
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
		scope, _, err := store.publication.ReadScope(store.handleNamespace())
		if err != nil {
			return err
		}
		scope.NextChangeStream = 0
		return store.transaction.WriteScope(scope)
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

func TestMChangesCorruptLaterAttemptKeepsSiblingAndRecoveryStrict(t *testing.T) {
	for _, view := range []string{"", "summary", "history", "list", "net"} {
		t.Run(view, func(t *testing.T) {
			t.Parallel()
			f := newMChangesSliceFixture(t, "corrupt-attempt")
			valid := f.reserve(t, f.thread, "valid")
			f.publish(t, valid, "valid", "valid-call", mekugiHistory{
				ReviewFiles: []mekugi.ReviewFile{mekugi.RenderReviewFile("kept.txt", "kept.txt", "old\n", "new\n")},
			})
			broken := f.reserve(t, f.thread, "broken")
			versions := []string{"old\n", "middle\n", "new\n"}
			for attempt, call := range []string{"first", "later"} {
				f.publish(t, broken, "broken", call, mekugiHistory{Attempt: attempt + 1,
					ReviewFiles: []mekugi.ReviewFile{mekugi.RenderReviewFile("discarded.txt", "discarded.txt", versions[attempt], versions[attempt+1])},
				})
			}
			options := changeReadOptions{workspace: f.workspace, ids: []string{broken, valid}, view: view}
			text, snapshot, err := f.store.readChangeView(f.ctx, options)
			if err != nil {
				t.Fatal(err)
			}
			reference, err := f.store.putChangeRead(f.ctx, snapshot, text, 0)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(f.store.directory, legacycall.Name(f.workspace, "later", false)), []byte("{"), 0600); err != nil {
				t.Fatal(err)
			}
			output, selected, err := f.store.readChangeView(f.ctx, options)
			if err == nil || !strings.Contains(err.Error(), broken) || len(selected.IDs) != 1 || selected.IDs[0] != valid ||
				strings.Contains(output, "discarded.txt") || strings.Contains(output, broken) {
				t.Fatalf("partial read: output=%q selection=%+v error=%v", output, selected, err)
			}
			marker := "kept.txt"
			if view == "list" {
				marker = valid
			}
			if !strings.Contains(output, marker) {
				t.Fatalf("valid sibling missing: %q", output)
			}
			if _, err := f.store.recoverOutput(f.ctx, reference); err == nil {
				t.Fatal("frozen recovery skipped corrupt evidence")
			}
		})
	}
}
