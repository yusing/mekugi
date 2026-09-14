package router

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
	"github.com/yusing/mekugi"
)

// Exercise the same snapshot path used by the event-stream consumer.
func (s *mekugiReplayStore) liveDiffSnapshotFiles(ctx context.Context, scope liveDiffScope) ([]liveDiffFile, error) {
	data, err := s.liveDiffSnapshot(ctx, scope)
	if err != nil {
		return nil, err
	}
	return data.files(), nil
}

func liveDiffScopeCapture(t *testing.T, store *mekugiReplayStore, workspace, thread, call, path, before, after string) {
	t.Helper()
	id, err := store.reserveChange(t.Context(), workspace, thread, call)
	if err != nil {
		t.Fatal(err)
	}
	history := mekugiHistory{ChangeID: id, CorrelationID: call, Applied: true,
		ReviewFiles: []mekugi.ReviewFile{{BeforePath: path, AfterPath: path,
			Diff: "--- \"" + path + "\"\n+++ \"" + path + "\"\n@@ -1 +1 @@\n-" + before + "\n+" + after + "\n"}}}
	if err := store.put(t.Context(), workspace, map[string]mekugiHistory{call: history}); err != nil {
		t.Fatal(err)
	}
}

func TestLiveDiffSessionScopeStreamsAndWorkspaces(t *testing.T) {
	workspace, second := t.TempDir(), t.TempDir()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	liveDiffScopeCapture(t, store, workspace, "unrelated", "old", filepath.Join(workspace, "old.go"), "a", "b")
	workspaces := map[string]map[string]bool{workspace: {"current": true}}
	read := func() []liveDiffFile {
		t.Helper()
		// A fresh reader has no in-memory ancestry or routing cache.
		reader := &mekugiReplayStore{directory: store.directory}
		files, err := reader.liveDiffSnapshotFiles(t.Context(), liveDiffScope{Workspaces: workspaces})
		if err != nil {
			t.Fatal(err)
		}
		return files
	}
	if files := read(); len(files) != 0 {
		t.Fatal("new session inherited unrelated workspace edits")
	}
	tempPath := filepath.Join(t.TempDir(), "notes.txt")
	liveDiffScopeCapture(t, store, workspace, "current", "current-call", tempPath, "old", "new")
	if files := read(); len(files) != 1 || files[0].path != tempPath {
		t.Fatalf("outside-workspace edit missing: %#v", files)
	}
	liveDiffScopeCapture(t, store, second, "child", "child-call", filepath.Join(second, "child.txt"), "old", "new")
	workspaces[second] = map[string]bool{"child": true}
	if files := read(); len(files) != 2 {
		t.Fatalf("child/second workspace missing: %#v", files)
	}
	liveDiffScopeCapture(t, store, workspace, "unrelated", "other", filepath.Join(workspace, "other.go"), "a", "b")
	if files := read(); len(files) != 2 {
		t.Fatal("concurrent unrelated session leaked into view")
	}
}

func TestLiveDiffSessionCrossWorkspaceOverlap(t *testing.T) {
	root := t.TempDir()
	first, second := filepath.Join(root, "z"), filepath.Join(root, "a")
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	scope := liveDiffScope{Workspaces: map[string]map[string]bool{first: {"root": true}, second: {"child": true}}}
	path := filepath.Join(t.TempDir(), "notes.txt")
	liveDiffScopeCapture(t, store, first, "root", "same-call-id", path, "original", "first")
	view := liveDiffView{scroll: make(map[string]int)}
	refresh := func() {
		t.Helper()
		files, err := store.liveDiffSnapshotFiles(t.Context(), scope)
		if err != nil {
			t.Fatal(err)
		}
		view.merge(files)
		view.refreshVisible()
	}
	refresh()
	key := view.files[0].key()
	view.scroll[key] = 2
	view.flush(true)
	liveDiffScopeCapture(t, store, second, "child", "same-call-id", path, "first", "fixed")
	refresh()
	if len(view.files) != 1 || len(view.files[0].chunks) != 2 || view.files[0].key() != key || view.scroll[key] != 2 {
		t.Fatal("workspace switch broke shared file identity, selection or call identity")
	}
	if chunks := view.visible[key].chunks; len(chunks) != 1 || !strings.Contains(chunks[0].review.Diff, "-original\n+fixed\n") {
		t.Fatalf("cross-workspace edit lost its combined result: %#v", chunks)
	}
	liveDiffScopeCapture(t, store, second, "child", "revert", path, "fixed", "original")
	refresh()
	if chunks := view.visible[key].chunks; len(chunks) != 0 {
		t.Fatal("cross-workspace full revert retained a net diff")
	}
	view.flush(true)
	if len(view.visible[key].chunks) != 0 {
		t.Fatal("flush retained reviewed captures")
	}
}

func TestLiveDiffFreshSnapshotComposesCrossStreamCaptures(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprintf("legacy=%t", legacy), func(t *testing.T) {
			workspace := t.TempDir()
			store, err := openMekugiReplayStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			// Allocate A before B, but publish B's target edit before A's target edit.
			liveDiffScopeCapture(t, store, workspace, "A", "unrelated", filepath.Join(workspace, "other"), "old", "new")
			path := filepath.Join(workspace, "target")
			for _, capture := range []struct{ thread, call, diff string }{
				{"B", "insert", "--- target\n+++ target\n@@ -0,0 +1 @@\n+prefix\n"},
				{"A", "replace", "--- target\n+++ target\n@@ -21 +21 @@\n-old\n+new\n"},
			} {
				id, err := store.reserveChange(t.Context(), workspace, capture.thread, capture.call)
				if err != nil {
					t.Fatal(err)
				}
				history := mekugiHistory{ChangeID: id, CorrelationID: capture.call, Applied: true,
					ReviewFiles: []mekugi.ReviewFile{{BeforePath: path, AfterPath: path, Diff: capture.diff}}}
				if err := store.put(t.Context(), workspace, map[string]mekugiHistory{capture.call: history}); err != nil {
					t.Fatal(err)
				}
				if legacy {
					record, found, err := store.read(workspace, capture.call, false)
					if err != nil || !found {
						t.Fatalf("read capture: found=%t err=%v", found, err)
					}
					record.CaptureOrder = 0
					data, err := json.Marshal(record)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(store.directory, replayRecordName(workspace, capture.call, false)), data, 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			files, err := store.liveDiffSnapshotFiles(t.Context(), liveDiffScope{Workspaces: map[string]map[string]bool{workspace: nil}})
			if err != nil {
				t.Fatal(err)
			}
			var view liveDiffView
			view.merge(files)
			view.refreshVisible()
			for _, file := range view.files {
				if file.path == path {
					chunks := view.visible[file.key()].chunks
					if legacy {
						if len(chunks) != 1 || !strings.Contains(chunks[0].status, "older captures have no shared order") || chunks[0].review.Diff != "" {
							t.Fatalf("legacy captures guessed a result or rendered individual patches: %#v", chunks)
						}
						return
					}
					text := liveDiffVisibleText(view.visible[file.key()])
					if len(chunks) != 2 || chunks[0].status != "" || chunks[1].status != "" ||
						!strings.Contains(text, "+prefix\n") || !strings.Contains(text, "@@ -20,1 +21,1 @@\n-old\n+new\n") {
						t.Fatalf("cross-stream result used stream order instead of capture order: %s", text)
					}
					return
				}
			}
			t.Fatal("target missing from snapshot")
		})
	}
}

func TestLiveDiffSessionTerminalEmptyEditsAndExit(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	workspace := t.TempDir()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	liveDiffScopeCapture(t, store, workspace, "other", "old", filepath.Join(workspace, "unrelated.go"), "old", "new")
	connection, broker, stopBroker := liveDiffTestBroker(t, store, liveDiffScope{Workspaces: map[string]map[string]bool{workspace: {"current": true}}})
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLiveDiffTerminalProcess$")
	cmd.Env = append(os.Environ(), "MEKUGI_LIVE_DIFF_TEST_CHILD=1",
		"MEKUGI_LIVE_DIFF_WORKSPACE="+workspace, "MEKUGI_LIVE_DIFF_REPLAY="+store.directory,
		"MEKUGI_LIVE_DIFF_SESSION="+connection)
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 25, Cols: 120})
	if err != nil {
		t.Fatal(err)
	}
	defer terminal.Close()
	chunks := make(chan string, 64)
	go func() {
		defer close(chunks)
		var buf [8192]byte
		for {
			n, err := terminal.Read(buf[:])
			if n > 0 {
				select {
				case chunks <- string(buf[:n]):
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	wait := func(want string) string {
		t.Helper()
		var text strings.Builder
		for {
			select {
			case chunk, ok := <-chunks:
				if !ok {
					t.Fatalf("viewer stopped waiting for %q: %s", want, text.String())
				}
				text.WriteString(chunk)
				if strings.Contains(ansi.Strip(text.String()), want) {
					if strings.Contains(text.String(), "unrelated.go") {
						t.Fatal("viewer displayed another session")
					}
					return ansi.Strip(text.String())
				}
			case <-ctx.Done():
				t.Fatalf("waiting for %q: %s", want, text.String())
			}
		}
	}
	wait("Waiting for captured")
	path := filepath.Join(t.TempDir(), "notes.txt")
	liveDiffScopeCapture(t, store, workspace, "current", "edit", path, "original", "created")
	wait("+created")
	if _, err := terminal.Write([]byte("f")); err != nil {
		t.Fatal(err)
	}
	wait("No unreviewed changes")
	liveDiffScopeCapture(t, store, workspace, "current", "fix", path, "created", "updated")
	text := wait("+updated")
	if !strings.Contains(text, "-original") {
		t.Fatalf("flush lost original-to-latest integrity: %s", text)
	}
	// A scope update subscribes a second workspace without restarting the
	// viewer, including captures published before that membership arrived.
	second := t.TempDir()
	childPath := filepath.Join(second, "child.txt")
	liveDiffScopeCapture(t, store, second, "child", "child-first", childPath, "child-original", "child-created")
	broker.setScope(liveDiffScope{Workspaces: map[string]map[string]bool{workspace: {"current": true}, second: {"child": true}}})
	wait("+child-created")
	liveDiffScopeCapture(t, store, second, "child", "child-next", childPath, "child-created", "child-updated")
	wait("+child-updated")
	stopBroker()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("viewer outlived session")
	}
}
