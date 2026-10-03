package router

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
	"github.com/yusing/mekugi"
	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/ui/diffview"
)

func nativeBatchFixture(t *testing.T) *appServerUI {
	t.Helper()
	u, _ := newAppServerTestUI()
	u.clock = func() time.Time { return time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local) }
	u.view.painter.Theme = livediff.DarkTheme
	u.status, u.model, u.reasoningEffort = "Ready", "snapshot-model", "high"
	u.session.cwd = "/workspace"
	u.ensureShell()
	u.view.clock, u.agents.clock = u.clock, u.clock
	u.shell.journalOpen = false
	t.Cleanup(func() { _ = u.shell.diffScreen.Close() })
	u.view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: 1, Agent: "Main", Kind: "text", Text: "MAIN_TRANSCRIPT", Observed: u.clock()}}})
	u.agents.apply(activityPaneEvent{Kind: "snapshot", Agents: []activityPaneAgent{{Name: "/root/editor"}, {Name: "/root/reviewer"}}, Entries: []activityPaneEntry{
		{Seq: 1, Agent: "/root/editor", Kind: "commentary", Text: "EDITOR_TRANSCRIPT", Observed: u.clock()},
		{Seq: 2, Agent: "/root/reviewer", Kind: "commentary", Text: "OTHER_ACTIVITY remains readable", Observed: u.clock()},
	}})
	return u
}

func nativeBatchPreview(id, caller string, names ...string) diffview.Preview {
	p := diffview.Preview{ID: id, Caller: caller, Workspace: "/workspace", Status: diffview.PreviewEdit}
	for _, name := range names {
		var source strings.Builder
		for i := 1; i <= 40; i++ {
			fmt.Fprintf(&source, "%s_line_%02d\n", name, i)
		}
		p.Files = append(p.Files, mekugi.RenderReviewFile("", "/workspace/"+name+".txt", "", source.String()))
	}
	return p
}

func TestUISnapshotNativeBatchPlacement(t *testing.T) {
	u := nativeBatchFixture(t)
	screen := vt.NewEmulator(140, 48)
	defer screen.Close()
	paint := func(name string, width, height int) {
		t.Helper()
		screen.Resize(width, height)
		if err := u.paint(screen, width, height); err != nil {
			t.Fatal(err)
		}
		t.Run(name, func(t *testing.T) { assertNativeUISnapshot(t, name, strings.Split(screen.String(), "\n")) })
	}
	u.shell.preview(nativeBatchPreview("child", "/root/editor", "child"))
	revealNativeDock(u.shell)
	paint("native-batch-child-replaces-transcript", 140, 48)
	u.shell.preview(nativeBatchPreview("main", "/root", "one", "two", "three"))
	revealNativeDock(u.shell)
	paint("native-batch-main-overflow", 140, 48)
	u.shell.nextLive()
	paint("native-batch-main-pinned", 140, 48)
	paint("native-batch-short-summary", 80, 18)
	u.shell.focus = 2
	paint("native-batch-narrow-activity", 80, 28)
	u.shell.preview(diffview.Preview{ID: "child", Workspace: "/workspace"})
	revealNativeDock(u.shell)
	u.shell.preview(diffview.Preview{ID: "main", Workspace: "/workspace"})
	revealNativeDock(u.shell)
	paint("native-batch-restored-transcripts", 140, 48)
}

func TestNativeBatchLivePTY(t *testing.T) {
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer slave.Close()
	frames := make(chan []byte, 16)
	go func() {
		var pending synchronizedFrameBuffer
		buf := make([]byte, 8192)
		for {
			n, err := master.Read(buf)
			pending.Append(buf[:n])
			for {
				frame, ok := pending.Next()
				if !ok {
					break
				}
				frames <- append([]byte(nil), frame...)
			}
			if err != nil {
				return
			}
		}
	}()
	u := nativeBatchFixture(t)
	screen := vt.NewEmulator(140, 48)
	defer screen.Close()
	paint := func(width, height int) string {
		t.Helper()
		if err := pty.Setsize(master, &pty.Winsize{Cols: uint16(width), Rows: uint16(height)}); err != nil {
			t.Fatal(err)
		}
		screen.Resize(width, height)
		if err := u.paint(slave, width, height); err != nil {
			t.Fatal(err)
		}
		select {
		case frame := <-frames:
			if _, err := screen.Write(frame); err != nil {
				t.Fatal(err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("PTY frame timeout")
		}
		return screen.String()
	}
	// A quick completed edit must never produce even one transcript-replacing
	// frame on the real terminal, including after its reveal deadline.
	quick := nativeBatchPreview("quick", "/root", "quick")
	u.shell.preview(quick)
	if frame := paint(140, 48); strings.Contains(frame, "LIVE ·") || !strings.Contains(frame, "MAIN_TRANSCRIPT") {
		t.Fatal("pending edit flashed a dock")
	}
	quick.Complete = true
	u.shell.preview(quick)
	revealNativeDock(u.shell)
	if frame := paint(140, 48); strings.Contains(frame, "LIVE ·") || !strings.Contains(frame, "MAIN_TRANSCRIPT") {
		t.Fatal("completed quick edit popped a dock")
	}
	deleted := diffview.Preview{ID: "deleted", Caller: "/root", Workspace: "/workspace", Status: diffview.PreviewEdit,
		Files: []mekugi.ReviewFile{mekugi.RenderReviewFile("/workspace/deleted.go", "", "package old\n", "")}}
	u.shell.preview(deleted)
	revealNativeDock(u.shell)
	if frame := paint(140, 48); strings.Contains(frame, "LIVE ·") || !strings.Contains(frame, "MAIN_TRANSCRIPT") {
		t.Fatal("deletion-only call replaced transcript with an empty dock")
	}
	u.shell.preview(diffview.Preview{ID: "deleted", Workspace: "/workspace"})
	p := nativeBatchPreview("main", "/root", "one")
	u.shell.preview(p)
	revealNativeDock(u.shell)
	u.shell.preview(nativeBatchPreview("child", "/root/editor", "child"))
	revealNativeDock(u.shell)
	frame := paint(140, 48)
	if !strings.Contains(frame, "one_line_40") || !strings.Contains(frame, "child_line_40") || !strings.Contains(frame, "OTHER_ACTIVITY") || strings.Contains(frame, "MAIN_TRANSCRIPT") || strings.Contains(frame, "EDITOR_TRANSCRIPT") {
		t.Fatalf("wrong caller replacement:\n%s", frame)
	}
	for _, key := range []byte("\x02\x1b[5~") {
		if err := u.shell.key(key); err != nil {
			t.Fatal(err)
		}
	}
	frame = paint(140, 48)
	if strings.Contains(frame, "one_line_40") {
		t.Fatal("source scroll did not pause live viewport")
	}
	var grown strings.Builder
	for i := 1; i <= 40; i++ {
		fmt.Fprintf(&grown, "one_line_%02d\n", i)
	}
	p.Files[0] = mekugi.RenderReviewFile("", "/workspace/one.txt", "", grown.String()+"streaming_tip\n")
	u.shell.preview(p)
	revealNativeDock(u.shell)
	if frame = paint(140, 48); strings.Contains(frame, "streaming_tip") {
		t.Fatal("new input stole paused viewport")
	}
	for _, key := range []byte("\x02r") {
		if err := u.shell.key(key); err != nil {
			t.Fatal(err)
		}
	}
	if frame = paint(140, 48); !strings.Contains(frame, "streaming_tip") {
		t.Fatal("resume did not follow source tip")
	}
	p.Complete = true
	u.shell.preview(p)
	revealNativeDock(u.shell)
	u.shell.preview(nativeBatchPreview("next", "/root", "two", "three"))
	revealNativeDock(u.shell)
	if len(u.shell.liveDock.Order) != 3 {
		t.Fatal("new edit evicted completed batch member")
	}
	frame = paint(140, 48)
	if !strings.Contains(frame, "three_line_40") || strings.Contains(frame, "one_line_40") {
		t.Fatal("full screen did not introduce newest file")
	}
	if frame = paint(80, 18); !strings.Contains(frame, "enlarge for diff") || strings.Contains(frame, "three_line") {
		t.Fatal("short terminal squeezed below minimum file height")
	}
	u.shell.preview(diffview.Preview{ID: "next"})
	revealNativeDock(u.shell)
	u.shell.animating(time.Now().Add(nativeDockMinimum + time.Second))
	frame = paint(140, 48)
	if !strings.Contains(frame, "MAIN_TRANSCRIPT") || !strings.Contains(frame, "child_line_40") {
		t.Fatal("batch close affected another caller or failed to restore transcript")
	}
	u.shell.preview(diffview.Preview{ID: "child", Workspace: "/workspace"})
	revealNativeDock(u.shell)
	if frame = paint(140, 48); !strings.Contains(frame, "EDITOR_TRANSCRIPT") || strings.Contains(frame, "LIVE ·") {
		t.Fatal("withdrawal did not restore Activity")
	}
}

func TestUISnapshotNativeBatchDeletionPresentation(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		name := "native-batch-deletion-only"
		if mixed {
			name = "native-batch-deletion-mixed"
		}
		t.Run(name, func(t *testing.T) {
			u := nativeBatchFixture(t)
			preview := diffview.Preview{ID: "deleted", Caller: "/root", Workspace: "/workspace", Status: diffview.PreviewEdit,
				Files: []mekugi.ReviewFile{mekugi.RenderReviewFile("/workspace/deleted.go", "", "package old\n", "")}}
			if mixed {
				preview.Files = append(preview.Files, nativeBatchPreview("kept", "/root", "kept").Files...)
			}
			u.shell.preview(preview)
			revealNativeDock(u.shell)
			screen := vt.NewEmulator(140, 48)
			defer screen.Close()
			if err := u.paint(screen, 140, 48); err != nil {
				t.Fatal(err)
			}
			assertNativeUISnapshot(t, name, strings.Split(screen.String(), "\n"))
		})
	}
}
