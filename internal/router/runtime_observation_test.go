package router

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func nativeObservationFixture(t *testing.T) (*nativeObservationOwner, *mekugiReplayStore, ObservationBinding) {
	t.Helper()
	workspace := t.TempDir()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	owner, err := newNativeObservationOwner(t.Context(), store, "claude", workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(owner.close)
	binding := ObservationBinding{Runtime: "claude", Session: "native-session", Workspace: workspace}
	if err := owner.bind(t.Context(), binding); err != nil {
		t.Fatal(err)
	}
	return owner, store, binding
}

func nativeObservationWrite(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}

func nativeObservationHistory(t *testing.T, store *mekugiReplayStore, call ObservationCall, phase string) mekugiHistory {
	t.Helper()
	history, found, err := store.lookup(t.Context(), call.Binding.Workspace, observationKey(call)+"/"+phase)
	if err != nil || !found {
		t.Fatalf("durable %s record missing: found=%v err=%v", phase, found, err)
	}
	return history
}

func TestNativeObservationActualOutcomesAndIdempotence(t *testing.T) {
	for _, status := range []string{"completed", "failed", "stopped"} {
		t.Run(status, func(t *testing.T) {
			owner, store, binding := nativeObservationFixture(t)
			path := filepath.Join(binding.Workspace, "source.go")
			nativeObservationWrite(t, path, "original\n")
			call := ObservationCall{Binding: binding, ID: "native-tool-id", Tool: "Edit", Input: `{"old_string":"original","new_string":"partial"}`, Paths: []string{path}}
			if err := owner.before(t.Context(), call); err != nil {
				t.Fatal(err)
			}
			// A separate reader sees the baseline before before() acknowledges it.
			reader, err := openMekugiReplayStore(store.directory)
			if err != nil {
				t.Fatal(err)
			}
			baseline := nativeObservationHistory(t, reader, call, "before")
			if baseline.ExecObservation == nil || len(baseline.ExecObservation.Files) != 1 || baseline.NativeObservation == nil || !reflect.DeepEqual(baseline.NativeObservation.Call, &call) {
				t.Fatalf("pre-tool baseline/input not retained: %#v", baseline)
			}
			nativeObservationWrite(t, path, "partial\n")
			if err := owner.before(t.Context(), call); err != nil {
				t.Fatal(err)
			}
			terminal := ObservationTerminal{Status: status, Report: "native result"}
			id, err := owner.after(t.Context(), call, terminal)
			if err != nil || id == "" {
				t.Fatalf("actual %s edit lost: id=%q err=%v", status, id, err)
			}
			history := nativeObservationHistory(t, reader, call, "after")
			if history.ChangeID != id || history.ToolName != "Edit" || history.Source != "Edit" || history.ExecOutcome == nil || history.ExecOutcome.Status != status || len(history.ReviewFiles) != 1 {
				t.Fatalf("native attribution/outcome lost: %#v", history)
			}
			if diff := history.ReviewFiles[0].Diff; !strings.Contains(diff, "-original\n+partial\n") {
				t.Fatalf("actual partial effect not retained: %s", diff)
			}
			nativeObservationWrite(t, path, "unrelated later write\n")
			if err := owner.before(t.Context(), call); err != nil {
				t.Fatal(err)
			}
			repeated, err := owner.after(t.Context(), call, terminal)
			if err != nil || repeated != id {
				t.Fatalf("replay allocated another ID: %q %v", repeated, err)
			}
			if replay := nativeObservationHistory(t, reader, call, "after"); !reflect.DeepEqual(history, replay) {
				t.Fatal("duplicate completion changed durable evidence")
			}
			changed := call
			changed.Input = `{"changed":true}`
			if err := owner.before(t.Context(), changed); err == nil {
				t.Fatal("completed call accepted changed input")
			}
			if id, err := owner.after(t.Context(), changed, terminal); err == nil || id != "" {
				t.Fatalf("completed call accepted changed terminal input: %q %v", id, err)
			}
			scope := liveDiffScope{Workspaces: map[string]map[string]bool{binding.Workspace: {observationThread(binding): true}}}
			files, err := reader.liveDiffSnapshotFiles(t.Context(), scope)
			if err != nil || len(files) != 1 || len(files[0].Chunks) != 1 {
				t.Fatalf("shared Diff duplicate/missing capture: %#v %v", files, err)
			}
		})
	}
}

func TestNativeObservationNoEffectHasNoChangeID(t *testing.T) {
	for _, status := range []string{"completed", "failed", "stopped"} {
		t.Run(status, func(t *testing.T) {
			owner, store, binding := nativeObservationFixture(t)
			path := filepath.Join(binding.Workspace, "unchanged.txt")
			nativeObservationWrite(t, path, "unchanged\n")
			call := ObservationCall{Binding: binding, ID: "denied-or-noop", Tool: "Write", Input: `{}`, Paths: []string{path}}
			if err := owner.before(t.Context(), call); err != nil {
				t.Fatal(err)
			}
			id, err := owner.after(t.Context(), call, ObservationTerminal{Status: status, Report: "denied or unchanged"})
			if err != nil || id != "" {
				t.Fatalf("unchanged file received ID: %q %v", id, err)
			}
			history := nativeObservationHistory(t, store, call, "after")
			if len(history.ReviewFiles) != 0 {
				t.Fatalf("no effect fabricated review: %#v", history.ReviewFiles)
			}
		})
	}
}

func TestNativeObservationExplicitIgnoredSource(t *testing.T) {
	// This disposable repository proves the path is actually Git-ignored,
	// rather than merely naming an otherwise ordinary source "ignored".
	workspace := newExecVCSTestRepo(t, map[string]string{".gitignore": "ignored.txt\n"})
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	owner, err := newNativeObservationOwner(t.Context(), store, "claude", workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(owner.close)
	binding := ObservationBinding{Runtime: "claude", Session: "native-session", Workspace: workspace}
	if err := owner.bind(t.Context(), binding); err != nil {
		t.Fatal(err)
	}
	if ignored := runExecVCSTestGit(t, workspace, "check-ignore", "ignored.txt"); strings.TrimSpace(string(ignored)) != "ignored.txt" {
		t.Fatalf("fixture path not ignored: %q", ignored)
	}
	path := filepath.Join(binding.Workspace, "ignored.txt")
	nativeObservationWrite(t, path, "old\n")
	call := ObservationCall{Binding: binding, ID: "ignored-write", Tool: "Write", Input: `{"file_path":"ignored.txt"}`, Paths: []string{"ignored.txt"}}
	if err := owner.before(t.Context(), call); err != nil {
		t.Fatal(err)
	}
	nativeObservationWrite(t, path, "new\n")
	id, err := owner.after(t.Context(), call, ObservationTerminal{Status: "completed"})
	if err != nil || id == "" {
		t.Fatalf("explicit ignored source not recorded: %q %v", id, err)
	}
	history := nativeObservationHistory(t, store, call, "after")
	if len(history.ReviewFiles) != 1 || history.ReviewFiles[0].AfterPath != path || history.ToolName != "Write" || history.Source != "Write" {
		t.Fatalf("explicit native source lost: %#v", history)
	}
}

func TestNativeObservationRejectsUntrustedIdentityAndChangedInput(t *testing.T) {
	owner, _, binding := nativeObservationFixture(t)
	invalid := []ObservationBinding{
		{Runtime: "codex", Session: binding.Session, Workspace: binding.Workspace},
		{Runtime: binding.Runtime, Workspace: binding.Workspace},
		{Runtime: binding.Runtime, Session: "other-session", Workspace: binding.Workspace},
		{Runtime: binding.Runtime, Session: binding.Session, Workspace: t.TempDir()},
		{Runtime: binding.Runtime, Session: binding.Session, Workspace: binding.Workspace, Branch: "unverified"},
	}
	for _, bad := range invalid {
		if err := owner.bind(t.Context(), bad); err == nil {
			t.Fatalf("invalid binding accepted: %#v", bad)
		}
		if err := owner.before(t.Context(), ObservationCall{Binding: bad, ID: "bad", Tool: "Edit", Input: `{}`}); err == nil {
			t.Fatalf("unbound call accepted: %#v", bad)
		}
	}
	unknown := binding
	unknown.Agent = "unbound-child"
	call := ObservationCall{Binding: unknown, ID: "unknown-agent", Tool: "Edit", Input: `{}`}
	if err := owner.before(t.Context(), call); err == nil {
		t.Fatal("unknown agent accepted")
	}
	if id, err := owner.after(t.Context(), call, ObservationTerminal{Status: "completed"}); err == nil || id != "" {
		t.Fatalf("unknown agent completion accepted: %q %v", id, err)
	}
	call.Binding = binding
	call.ID = "stable-call"
	if err := owner.before(t.Context(), call); err != nil {
		t.Fatal(err)
	}
	call.Input = `{"different":true}`
	if err := owner.before(t.Context(), call); err == nil {
		t.Fatal("changed pre-tool input accepted")
	}
	if id, err := owner.after(t.Context(), call, ObservationTerminal{Status: "completed"}); err == nil || id != "" {
		t.Fatalf("changed terminal input accepted: %q %v", id, err)
	}
}

func TestNativeObservationCallsAndAgentScopesAreIsolated(t *testing.T) {
	owner, store, root := nativeObservationFixture(t)
	child := root
	child.Agent = "native-child"
	if err := owner.bind(t.Context(), child); err != nil {
		t.Fatal(err)
	}
	calls := []ObservationCall{
		{Binding: root, ID: "same-native-id", Tool: "Edit", Input: `{}`, Paths: []string{"root.txt"}},
		{Binding: child, ID: "same-native-id", Tool: "Write", Input: `{}`, Paths: []string{"child.txt"}},
	}
	for _, call := range calls {
		nativeObservationWrite(t, filepath.Join(root.Workspace, call.Paths[0]), "old\n")
		if err := owner.before(t.Context(), call); err != nil {
			t.Fatal(err)
		}
	}
	ids := make(map[string]bool)
	for _, call := range calls {
		path := filepath.Join(root.Workspace, call.Paths[0])
		nativeObservationWrite(t, path, "new\n")
		id, err := owner.after(t.Context(), call, ObservationTerminal{Status: "completed"})
		if err != nil || id == "" || ids[id] {
			t.Fatalf("call scopes reused/lost ID: %q %v", id, err)
		}
		ids[id] = true
		history := nativeObservationHistory(t, store, call, "after")
		if len(history.ReviewFiles) != 1 || history.ReviewFiles[0].AfterPath != path || history.Source != call.Tool {
			t.Fatalf("sibling scope leaked: %#v", history)
		}
		files, err := store.liveDiffSnapshotFiles(t.Context(), liveDiffScope{Workspaces: map[string]map[string]bool{root.Workspace: {observationThread(call.Binding): true}}})
		if err != nil || len(files) != 1 || files[0].Path != path {
			t.Fatalf("agent Diff scope leaked: %#v %v", files, err)
		}
	}
}

func TestNativeObservationRestartRestoresDiffButNotProcessWindow(t *testing.T) {
	owner, store, binding := nativeObservationFixture(t)
	path := filepath.Join(binding.Workspace, "finished.txt")
	nativeObservationWrite(t, path, "old\n")
	finished := ObservationCall{Binding: binding, ID: "finished", Tool: "Edit", Input: `{}`, Paths: []string{path}}
	if err := owner.before(t.Context(), finished); err != nil {
		t.Fatal(err)
	}
	nativeObservationWrite(t, path, "new\n")
	id, err := owner.after(t.Context(), finished, ObservationTerminal{Status: "completed"})
	if err != nil || id == "" {
		t.Fatalf("finished capture: %q %v", id, err)
	}
	processPath := filepath.Join(binding.Workspace, "process.txt")
	nativeObservationWrite(t, processPath, "before process\n")
	pending := ObservationCall{Binding: binding, ID: "process", Tool: "Bash", Input: `{"command":"echo process"}`, Command: "printf 'process\\n' > process.txt", Shell: "bash"}
	if err := owner.before(t.Context(), pending); err != nil {
		t.Fatal(err)
	}
	if id, err := owner.after(t.Context(), pending, ObservationTerminal{Status: "running", Task: "native-task"}); err != nil || id != "" {
		t.Fatalf("running observation: %q %v", id, err)
	}
	owner.close()
	nativeObservationWrite(t, processPath, "disconnected change\n")
	freshStore, err := openMekugiReplayStore(store.directory)
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := newNativeObservationOwner(t.Context(), freshStore, binding.Runtime, binding.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(fresh.close)
	if err := fresh.bind(t.Context(), binding); err != nil {
		t.Fatal(err)
	}
	files, err := freshStore.liveDiffSnapshotFiles(t.Context(), liveDiffScope{Workspaces: map[string]map[string]bool{binding.Workspace: {observationThread(binding): true}}})
	if err != nil || len(files) != 1 || files[0].Path != path {
		t.Fatalf("fresh Diff failed to restore: %#v %v", files, err)
	}
	if err := fresh.before(t.Context(), pending); err != nil {
		t.Fatal(err)
	}
	if id, err := fresh.after(t.Context(), pending, ObservationTerminal{Status: "completed", Task: "native-task"}); err != nil || id != "" {
		t.Fatalf("restart revived process observation: %q %v", id, err)
	}
	history := nativeObservationHistory(t, freshStore, pending, "after")
	if len(history.ReviewFiles) != 0 || history.ExecOutcome == nil || history.ExecOutcome.Coverage == execCoverageExact {
		t.Fatalf("restart falsely claims observed coverage: outcome=%#v reviews=%#v", history.ExecOutcome, history.ReviewFiles)
	}
}

func TestNativeObservationStorageFailureRetriesFrozenOutcome(t *testing.T) {
	owner, store, binding := nativeObservationFixture(t)
	path := filepath.Join(binding.Workspace, "retry.txt")
	nativeObservationWrite(t, path, "original\n")
	call := ObservationCall{Binding: binding, ID: "retry", Tool: "Edit", Input: `{}`, Paths: []string{path}}
	if err := owner.before(t.Context(), call); err != nil {
		t.Fatal(err)
	}
	nativeObservationWrite(t, path, "observed terminal\n")
	// Block change-index allocation using a real filesystem failure, independent
	// of permissions (tests may run as root). The baseline remains readable.
	blocker := filepath.Join(store.directory, changeIndexName(binding.Workspace, observationThread(binding)))
	if err := os.Mkdir(blocker, 0700); err != nil {
		t.Fatal(err)
	}
	terminal := ObservationTerminal{Status: "failed", Report: "partial edit"}
	if id, err := owner.after(t.Context(), call, terminal); err == nil || id != "" {
		t.Fatalf("storage failure exposed success/ID: %q %v", id, err)
	}
	nativeObservationWrite(t, path, "later unrelated content\n")
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	id, err := owner.after(t.Context(), call, terminal)
	if err != nil || id == "" {
		t.Fatalf("terminal retry failed: %q %v", id, err)
	}
	history := nativeObservationHistory(t, store, call, "after")
	if len(history.ReviewFiles) != 1 || !strings.Contains(history.ReviewFiles[0].Diff, "-original\n+observed terminal\n") || strings.Contains(history.ReviewFiles[0].Diff, "later unrelated") {
		t.Fatalf("retry reread changed disk: %#v", history.ReviewFiles)
	}
}

func TestNativeObservationBeforeStorageFailureDoesNotAcknowledgeBaseline(t *testing.T) {
	owner, store, binding := nativeObservationFixture(t)
	path := filepath.Join(binding.Workspace, "unacknowledged.txt")
	nativeObservationWrite(t, path, "old\n")
	call := ObservationCall{Binding: binding, ID: "before-storage-failure", Tool: "Edit", Input: `{}`, Paths: []string{path}}
	originalLimit := store.maxBytes
	store.maxBytes = 1
	if err := owner.before(t.Context(), call); err == nil {
		t.Fatal("failed baseline persistence was acknowledged")
	}
	store.maxBytes = originalLimit
	if _, found, err := store.lookup(t.Context(), binding.Workspace, observationKey(call)+"/before"); err != nil || found {
		t.Fatalf("failed pre-tool capture retained a baseline: found=%v err=%v", found, err)
	}
	nativeObservationWrite(t, path, "effect without acknowledged baseline\n")
	id, err := owner.after(t.Context(), call, ObservationTerminal{Status: "failed"})
	if err != nil || id != "" {
		t.Fatalf("unacknowledged pre-tool capture produced evidence: %q %v", id, err)
	}
}
