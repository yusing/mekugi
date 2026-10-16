//go:build codex_e2e

package router

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/internal/ui/diffview"
	"github.com/yusing/mekugi/libraries/diffmodel"
)

// Exercise overflow recovery through the native app-server event loop, with
// installed Codex and a local provider. Assertions consume the rendered PTY.
func TestAppServerPreviewResyncNativeCodex(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	proxy := newManagedMekugiProxy(t)
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	proxy.replayStore = store
	t.Cleanup(store.snapshots.close)
	auto, stop := newAutoLiveDiff(t.Context())
	defer stop()
	proxy.autoLiveDiff = auto
	before := func(t *testing.T, outer io.Writer, await func(string), awaitFrame func(func(string) bool), screen *vt.Emulator) {
		// A native shell fixture edit admits the real workspace/thread,
		// without racing thread creation or fabricating UI scope.
		if _, err := io.WriteString(outer, "!printf 'scope fixture\\n' > preview-scope.txt; printf 'PREVIEW_SCOPE_%s' READY\r"); err != nil {
			t.Fatal(err)
		}
		await("PREVIEW_SCOPE_READY")
		await("completed")
		auto.mu.Lock()
		workspace, thread := auto.workspace, ""
		for candidate := range auto.scope.Workspaces[workspace] {
			thread = candidate
		}
		auto.mu.Unlock()
		if workspace == "" || thread == "" {
			t.Fatal("native shell did not admit its workspace/thread")
		}
		preview := func(id string, rows int) diffview.Preview {
			var source strings.Builder
			for row := 1; row <= rows; row++ {
				fmt.Fprintf(&source, "%s_source_%04d\n", id, row)
			}
			return diffview.Preview{ID: id, Caller: "/root", Workspace: workspace, Thread: thread,
				Status: diffview.PreviewEdit, Tool: "apply_patch",
				Files: []diffmodel.ReviewFile{diffmodel.RenderReviewFile("", filepath.Join(workspace, id+".go"), "", source.String())}}
		}
		write := func(keys string) {
			t.Helper()
			if _, err := io.WriteString(outer, keys); err != nil {
				t.Fatal(err)
			}
		}
		// Holding the publication lock prevents draining between withdrawals.
		// Distinct IDs cannot coalesce, so this deterministically fills the real
		// native subscriber's bounded preview mailbox and closes its gap.
		overflow := func() {
			t.Helper()
			broker := auto.events
			broker.mu.Lock()
			defer broker.mu.Unlock()
			sub := broker.subscriber
			if sub == nil {
				t.Fatal("native UI preview subscriber missing")
			}
			for n := 0; n < 33; n++ {
				broker.emitPreviewLocked(diffview.Preview{ID: fmt.Sprintf("withdrawn-%d", n)})
			}
			select {
			case <-sub.gap:
			default:
				t.Fatal("preview mailbox overflow did not signal a gap")
			}
		}
		old, newest := preview("older", 80), preview("newest", 80)
		auto.events.publishPreview(old, false)
		await("older_source_0080")
		auto.events.publishPreview(newest, false)
		await("newest_source_0080")

		overflow()
		newest = preview("newest", 81)
		newest.Tool = "follow-resynced"
		old.Tool = "follow-resynced"
		auto.events.publishPreview(old, false)
		auto.events.publishPreview(newest, false)
		await("follow-resynced")
		// A key round-trip reaches the footer after the entire recovery paint,
		// rather than inspecting a PTY chunk midway through its source rows.
		write("\x02")
		awaitFrame(func(frame string) bool { return strings.Contains(frame, "ctrl+b 1-3") })
		if frame := screen.String(); !strings.Contains(frame, "newest_source_0081") || strings.Contains(frame, " · pinned ") {
			t.Fatalf("overflow lost newest-file following:\n%s", frame)
		}
		write("1")

		write("\x02e") // Cycle from newest to older and pin its source window.
		await("pinned")
		write(strings.Repeat("\x02\x1b[5~", 20)) // Page the source back to its first row.
		awaitFrame(func(frame string) bool {
			return strings.Contains(frame, "older_source_0001") && !strings.Contains(frame, "older_source_0080")
		})
		overflow()
		old = preview("older", 81)
		old.Tool = "pinned-resynced"
		newest.Tool = "pinned-resynced"
		auto.events.publishPreview(old, false)
		auto.events.publishPreview(newest, false)
		await("pinned-resynced")
		write("\x02")
		awaitFrame(func(frame string) bool { return strings.Contains(frame, "ctrl+b 1-3") })
		if frame := screen.String(); !strings.Contains(frame, " · pinned ") || !strings.Contains(frame, "older_source_0001") || strings.Contains(frame, "older_source_0081") {
			t.Fatalf("overflow moved the surviving pinned source viewport:\n%s", frame)
		}
		write("r")
		awaitFrame(func(frame string) bool {
			return strings.Contains(frame, "newest_source_0081") && !strings.Contains(frame, " · pinned ")
		})
		auto.events.publishPreview(old, true)
		auto.events.publishPreview(newest, true)
		awaitFrame(func(frame string) bool { return !strings.Contains(frame, "LIVE ·") })
	}
	runAppServerPreviewWith(t, &appPreviewProvider{}, proxy, appServerPreview{beforePrompt: before})
}
