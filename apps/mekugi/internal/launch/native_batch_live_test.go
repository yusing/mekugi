package launch

import (
	"fmt"
	"strings"
	"testing"
	"time"

	previewview "github.com/yusing/mekugi/modules/livepreview/terminalview"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"

	"github.com/yusing/mekugi/libraries/diffrender"

	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
	"github.com/yusing/mekugi/libraries/diffmodel"
)

func nativeBatchFixture(t *testing.T) *appServerUI {
	t.Helper()
	u, _ := newAppServerTestUI()
	u.clock = func() time.Time { return time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local) }
	u.shell.Transcript.Painter.Theme = diffrender.DarkTheme
	u.attached.presentation.Status.Text, u.model, u.reasoningEffort = "Ready", "snapshot-model", "high"
	u.attached.cwd = "/workspace"

	u.shell.Transcript.Clock, u.shell.Agents.Clock = u.clock, u.clock

	t.Cleanup(func() { _ = terminalTest(u).Diff.Close() })
	u.testActivity(u.shell.Transcript).Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{{Seq: 1, Agent: "Main", Kind: "text", Text: "MAIN_TRANSCRIPT", Observed: u.clock()}}})
	u.testActivity(u.shell.Agents).Coverage = ""
	u.testActivity(u.shell.Agents).Apply(sessionpresentation.ActivityEvent{Agents: []sessionpresentation.ActivityAgent{{Name: "/root/editor"}, {Name: "/root/reviewer"}}, Entries: []sessionpresentation.ActivityEntry{
		{Seq: 1, Agent: "/root/editor", Kind: "commentary", Text: "EDITOR_TRANSCRIPT", Observed: u.clock()},
		{Seq: 2, Agent: "/root/reviewer", Kind: "commentary", Text: "OTHER_ACTIVITY remains readable", Observed: u.clock()},
	}})
	return u
}

func nativeBatchPreview(id, caller string, names ...string) previewmodel.Preview {
	p := previewmodel.Preview{ID: id, Caller: caller, Workspace: "/workspace", Status: previewmodel.PreviewEdit}
	for _, name := range names {
		var source strings.Builder
		for i := 1; i <= 40; i++ {
			fmt.Fprintf(&source, "%s_line_%02d\n", name, i)
		}
		p.Files = append(p.Files, diffmodel.RenderReviewFile("", "/workspace/"+name+".txt", "", source.String()))
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
		if err := u.testPaint(screen, width, height); err != nil {
			t.Fatal(err)
		}
		t.Run(name, func(t *testing.T) { assertNativeUISnapshot(t, name, strings.Split(screen.String(), "\n")) })
	}
	applyPreview(&u.attached, u.shell, u.review, &u.pendingEditReceipts, presentationTime(u.clock), nativeBatchPreview("child", "/root/editor", "child"), false)
	revealNativeDock(terminalTest(u))
	u.testActivity(u.shell.Agents).setDisplayFilter(true, "/root/editor")
	paint("native-batch-child-replaces-transcript", 140, 48)
	u.testActivity(u.shell.Agents).setDisplayFilter(false, u.shell.Agents.Selected)
	paint("native-batch-main-child-only", 140, 48)
	applyPreview(&u.attached, u.shell, u.review, &u.pendingEditReceipts, presentationTime(u.clock), nativeBatchPreview("main", "/root", "one", "two", "three"), false)
	revealNativeDock(terminalTest(u))
	paint("native-batch-main-overflow", 140, 48)
	for _, key := range []byte("\x02e") {
		if err := terminalTest(u).key(key); err != nil {
			t.Fatal(err)
		}
	}
	paint("native-batch-main-pinned", 140, 48)
	paint("native-batch-short-summary", 80, 18)
	u.testActivity(u.shell.Agents).setDisplayFilter(true, "/root/editor")
	paint("native-batch-narrow-activity", 80, 28)
	applyPreview(&u.attached, u.shell, u.review, &u.pendingEditReceipts, presentationTime(u.clock), previewmodel.Preview{ID: "child", Workspace: "/workspace"}, false)
	revealNativeDock(terminalTest(u))
	applyPreview(&u.attached, u.shell, u.review, &u.pendingEditReceipts, presentationTime(u.clock), previewmodel.Preview{ID: "main", Workspace: "/workspace"}, false)
	revealNativeDock(terminalTest(u))
	paint("native-batch-restored-transcripts", 140, 48)
	applyPreview(&u.attached, u.shell, u.review, &u.pendingEditReceipts, presentationTime(u.clock), nativeBatchPreview("child", "/root/editor", "child", "second", "third"), false)
	revealNativeDock(terminalTest(u))
	for _, key := range []byte("\x02e") {
		if err := terminalTest(u).key(key); err != nil {
			t.Fatal(err)
		}
	}
	paint("native-batch-child-pinned", 140, 48)
	if !strings.Contains(screen.String(), "pinned") {
		t.Fatal("next live did not cycle the displayed child's files")
	}
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
		if err := u.testPaint(slave, width, height); err != nil {
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
	applyPreview(&u.attached, u.shell, u.review, &u.pendingEditReceipts, presentationTime(u.clock), quick, false)
	if frame := paint(140, 48); strings.Contains(frame, "LIVE ·") || !strings.Contains(frame, "MAIN_TRANSCRIPT") {
		t.Fatal("pending edit flashed a dock")
	}
	quick.Complete = true
	applyPreview(&u.attached, u.shell, u.review, &u.pendingEditReceipts, presentationTime(u.clock), quick, false)
	revealNativeDock(terminalTest(u))
	if frame := paint(140, 48); strings.Contains(frame, "LIVE ·") || !strings.Contains(frame, "MAIN_TRANSCRIPT") {
		t.Fatal("completed quick edit popped a dock")
	}
	deleted := previewmodel.Preview{ID: "deleted", Caller: "/root", Workspace: "/workspace", Status: previewmodel.PreviewEdit,
		Files: []diffmodel.ReviewFile{diffmodel.RenderReviewFile("/workspace/deleted.go", "", "package old\n", "")}}
	applyPreview(&u.attached, u.shell, u.review, &u.pendingEditReceipts, presentationTime(u.clock), deleted, false)
	revealNativeDock(terminalTest(u))
	if frame := paint(140, 48); strings.Contains(frame, "LIVE ·") || !strings.Contains(frame, "MAIN_TRANSCRIPT") {
		t.Fatal("deletion-only call replaced transcript with an empty dock")
	}
	applyPreview(&u.attached, u.shell, u.review, &u.pendingEditReceipts, presentationTime(u.clock), previewmodel.Preview{ID: "deleted", Workspace: "/workspace"}, false)
	p := nativeBatchPreview("main", "/root", "one")
	// A rewrite's not-yet-received baseline suffix must not displace its
	// arriving additions, including after publication clips a large diff.
	baseline := "retained\n" + strings.Repeat("old baseline source\n", 4000)
	var rewritten strings.Builder
	for i := 1; i <= 40; i++ {
		fmt.Fprintf(&rewritten, "one_line_%02d\n", i)
	}
	rewrite := func(source string) previewmodel.Preview {
		next := p
		next.Files = []diffmodel.ReviewFile{diffmodel.RenderReviewFile("/workspace/one.txt", "/workspace/one.txt", baseline, source+"retained\n")}
		return boundPreview(next)
	}
	p = rewrite(rewritten.String())
	applyPreview(&u.attached, u.shell, u.review, &u.pendingEditReceipts, presentationTime(u.clock), p, false)
	revealNativeDock(terminalTest(u))
	applyPreview(&u.attached, u.shell, u.review, &u.pendingEditReceipts, presentationTime(u.clock), nativeBatchPreview("child", "/root/editor", "child"), false)
	revealNativeDock(terminalTest(u))
	frame := paint(140, 48)
	if !strings.Contains(frame, "one_line_40") || !strings.Contains(frame, "child_line_40") || strings.Contains(frame, "OTHER_ACTIVITY") || strings.Contains(frame, "MAIN_TRANSCRIPT") || strings.Contains(frame, "EDITOR_TRANSCRIPT") {
		t.Fatalf("wrong caller replacement:\n%s", frame)
	}
	for _, key := range []byte("\x02\x1b[5~") {
		if err := terminalTest(u).key(key); err != nil {
			t.Fatal(err)
		}
	}
	frame = paint(140, 48)
	if strings.Contains(frame, "one_line_40") {
		t.Fatal("source scroll did not pause live viewport")
	}
	p = rewrite(rewritten.String() + "streaming_tip\n")
	applyPreview(&u.attached, u.shell, u.review, &u.pendingEditReceipts, presentationTime(u.clock), p, false)
	revealNativeDock(terminalTest(u))
	if frame = paint(140, 48); strings.Contains(frame, "streaming_tip") {
		t.Fatal("new input stole paused viewport")
	}
	for _, key := range []byte("\x02r") {
		if err := terminalTest(u).key(key); err != nil {
			t.Fatal(err)
		}
	}
	if frame = paint(140, 48); !strings.Contains(frame, "streaming_tip") {
		t.Fatal("resume did not follow source tip")
	}
	scroll := nativeBatchPreview("scroll", "/root", "scroll")
	scroll.Files = []diffmodel.ReviewFile{diffmodel.RenderReviewFile("/workspace/scroll.txt", "/workspace/scroll.txt",
		"retained\n"+strings.Repeat("old baseline source\n", 60), strings.Repeat("new source\n", 30)+"retained\n")}
	applyPreview(&u.attached, u.shell, u.review, &u.pendingEditReceipts, presentationTime(u.clock), scroll, false)
	paint(140, 48)
	pageDown := func() {
		for _, key := range []byte("\x02\x1b[6~") {
			if err := terminalTest(u).key(key); err != nil {
				t.Fatal(err)
			}
		}
	}
	for range 100 {
		pageDown()
	}
	bottom := paint(140, 48)
	for range 3 {
		pageDown()
		if got := paint(140, 48); got != bottom {
			t.Fatal("live diff shook after scrolling to the deleted suffix bottom")
		}
	}
	applyPreview(&u.attached, u.shell, u.review, &u.pendingEditReceipts, presentationTime(u.clock), previewmodel.Preview{ID: "scroll", Workspace: "/workspace"}, false)
	p.Complete = true
	applyPreview(&u.attached, u.shell, u.review, &u.pendingEditReceipts, presentationTime(u.clock), p, false)
	revealNativeDock(terminalTest(u))
	applyPreview(&u.attached, u.shell, u.review, &u.pendingEditReceipts, presentationTime(u.clock), nativeBatchPreview("next", "/root", "two", "three"), false)
	revealNativeDock(terminalTest(u))
	if len(terminalTest(u).Live.Order) != 3 {
		t.Fatal("new edit evicted completed batch member")
	}
	frame = paint(140, 48)
	if !strings.Contains(frame, "three_line_40") || strings.Contains(frame, "one_line_40") {
		t.Fatal("full screen did not introduce newest file")
	}
	if frame = paint(80, 18); !strings.Contains(frame, "enlarge for diff") || strings.Contains(frame, "three_line") {
		t.Fatal("short terminal squeezed below minimum file height")
	}
	applyPreview(&u.attached, u.shell, u.review, &u.pendingEditReceipts, presentationTime(u.clock), previewmodel.Preview{ID: "next"}, false)
	revealNativeDock(terminalTest(u))
	terminalTest(u).Live.Tick(time.Now().Add(previewview.CompletionHold + time.Second))
	frame = paint(140, 48)
	if strings.Contains(frame, "MAIN_TRANSCRIPT") || !strings.Contains(frame, "child_line_40") {
		t.Fatal("Main lost its surviving child preview")
	}
	u.testActivity(u.shell.Agents).setDisplayFilter(true, "/root/editor")
	if frame = paint(140, 48); !strings.Contains(frame, "child_line_40") {
		t.Fatal("child preview was lost while viewing Main")
	}
	applyPreview(&u.attached, u.shell, u.review, &u.pendingEditReceipts, presentationTime(u.clock), previewmodel.Preview{ID: "child", Workspace: "/workspace"}, false)
	revealNativeDock(terminalTest(u))
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
			preview := previewmodel.Preview{ID: "deleted", Caller: "/root", Workspace: "/workspace", Status: previewmodel.PreviewEdit,
				Files: []diffmodel.ReviewFile{diffmodel.RenderReviewFile("/workspace/deleted.go", "", "package old\n", "")}}
			if mixed {
				preview.Files = append(preview.Files, nativeBatchPreview("kept", "/root", "kept").Files...)
			}
			applyPreview(&u.attached, u.shell, u.review, &u.pendingEditReceipts, presentationTime(u.clock), preview, false)
			revealNativeDock(terminalTest(u))
			screen := vt.NewEmulator(140, 48)
			defer screen.Close()
			if err := u.testPaint(screen, 140, 48); err != nil {
				t.Fatal(err)
			}
			assertNativeUISnapshot(t, name, strings.Split(screen.String(), "\n"))
		})
	}
}
