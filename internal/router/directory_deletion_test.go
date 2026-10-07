package router

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/uisnapshot"
)

func TestUISnapshotDirectoryDeletion(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	attachTestReplayStore(t, proxy)
	workspace := t.TempDir()
	for _, path := range []string{"removed/a.txt", "removed/sub/b.txt", "removed/binary", "removed2/only.txt"} {
		writeTestFile(t, filepath.Join(workspace, path), "gone\n")
	}
	writeTestFile(t, filepath.Join(workspace, "removed/binary"), "\x00\xff")
	transform := prepareNativeStockTransform(t, proxy, workspace, "main")
	arguments := string(mustMarshalJSON(map[string]any{"cmd": "rm -rf removed removed2", "workdir": workspace}))
	retainCommandObservation(t, transform, "delete-call", arguments)
	if err := os.RemoveAll(filepath.Join(workspace, "removed")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(workspace, "removed2")); err != nil {
		t.Fatal(err)
	}
	reconcileExecItems(t, proxy, workspace, []any{
		map[string]any{"type": "function_call", "call_id": "delete-call", "name": nativeExecCommandToolName, "arguments": arguments},
		map[string]any{"type": "function_call_output", "call_id": "delete-call", "output": nativeExecOutput("Process exited with code 0")},
	})
	store := &mekugiReplayStore{directory: proxy.replayStore.directory}
	data, err := store.liveDiffSnapshot(t.Context(), liveDiffScope{Workspaces: map[string]map[string]bool{workspace: {"stock-thread": true}}})
	if err != nil {
		t.Fatal(err)
	}
	u := newAppServerSessionTestUI(t, workspace)
	u.thread = "stock-thread"
	u.session.start("stock-thread", workspace)
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "stock-thread", "turnId": "t", "item": map[string]any{
		"id": "delete-call", "type": "commandExecution", "command": "rm -rf removed removed2", "exitCode": 0}})
	u.shell.diff.data = data
	u.applyCapturedEdits()
	u.view.pace(u.view.now().Add(time.Second))
	var seq uint64
	for _, entry := range u.view.entries {
		if entry.native != nil && entry.native.item == "delete-call" {
			seq = entry.Seq
		}
	}
	if !u.shell.openActivityEdit(u.view, seq, "removed/") || len(u.shell.output.pages) != 4 {
		t.Fatal("directory link did not open all retained file pages")
	}
	uisnapshot.Assert(t, "testdata/snapshots/directory-deletion.txt", strings.Join(u.view.renderFeed(100, 30).lines, "\n")+"\n")
}
