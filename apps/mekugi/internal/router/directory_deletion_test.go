package router

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/backends/codex/binding"

	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"

	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func TestUISnapshotDirectoryDeletion(t *testing.T) {
	for _, contentGap := range []bool{false, true} {
		name := "directory-deletion"
		if contentGap {
			name += "-content-gap"
		}
		t.Run(name, func(t *testing.T) {
			proxy := newManagedMekugiProxy(t)
			attachTestReplayStore(t, proxy)
			workspace := t.TempDir()
			for _, path := range []string{"removed/a.txt", "removed/sub/b.txt", "removed/binary", "removed2/only.txt"} {
				writeTestFile(t, filepath.Join(workspace, path), "gone\n")
			}
			writeTestFile(t, filepath.Join(workspace, "removed/binary"), "\x00\xff")
			if contentGap {
				path := filepath.Join(workspace, "removed/large")
				writeTestFile(t, path, "")
				if err := os.Truncate(path, changecapture.MaxFileBytes+1); err != nil {
					t.Fatal(err)
				}
			}
			operation, _ := startShellObservationTest(t, proxy, workspace, "stock-thread", "t", "delete-call", "rm -rf removed removed2")
			if err := os.RemoveAll(filepath.Join(workspace, "removed")); err != nil {
				t.Fatal(err)
			}
			if err := os.RemoveAll(filepath.Join(workspace, "removed2")); err != nil {
				t.Fatal(err)
			}
			if err := operation.Complete(binding.ToolResult{CallID: "delete-call", Tool: "exec_command", Status: "completed", ExitCode: new(0)}, ""); err != nil {
				t.Fatal(err)
			}
			store := testReplayStore(mekugiReplayStore{directory: testProxyStore(proxy).directory})
			data, err := store.liveDiffSnapshot(t.Context(), previewmodel.Scope{Workspaces: map[string]map[string]bool{workspace: {"stock-thread": true}}})
			if err != nil {
				t.Fatal(err)
			}
			u := newAppServerSessionTestUI(t, workspace)
			u.Thread = "stock-thread"
			u.startSession("stock-thread", workspace)
			appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "stock-thread", "turnId": "t", "item": map[string]any{
				"id": "delete-call", "type": "commandExecution", "command": "rm -rf removed removed2", "exitCode": 0}})
			u.review.Projection.Data = data
			publishFixtureReview(t, u)
			u.applyCapturedEdits()
			u.testActivity(u.view).testPace(u.view.Now().Add(time.Second))
			var seq uint64
			for _, entry := range u.source.Entries {
				if entry.Source.Native != nil && entry.Source.Native.Item == "delete-call" {
					seq = entry.Source.Seq
				}
			}
			wantPages := 4
			if contentGap {
				wantPages++
			}
			if opened := u.shell.openActivityEdit(u.view, seq, "removed/"); !opened || len(u.shell.output.pages) != wantPages {
				t.Fatalf("directory pages: opened=%v seq=%d want=%d", opened, seq, wantPages)
			}
			uisnapshot.Assert(t, "testdata/snapshots/"+name+".txt", strings.Join(u.testActivity(u.view).RenderFeed(100, 30).Lines, "\n")+"\n")
		})
	}
}
