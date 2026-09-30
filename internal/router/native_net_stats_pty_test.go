package router

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
	"github.com/yusing/mekugi"
)

// Exercise durable capture -> controller -> native roster -> real PTY frames,
// including fresh-store replay, caller filtering, resize, and scope switching.
func TestNativeUINetStatsPTY(t *testing.T) {
	workspace := t.TempDir()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	publish := func(thread, caller, call, beforePath, afterPath, before, after string) {
		t.Helper()
		id, err := store.reserveChange(t.Context(), workspace, thread, call)
		if err != nil {
			t.Fatal(err)
		}
		history := mekugiHistory{ChangeID: id, CorrelationID: call, ExecutingThread: thread,
			Caller: caller, ReviewFiles: []mekugi.ReviewFile{mekugi.RenderReviewFile(beforePath, afterPath, before, after)}}
		if err := store.put(t.Context(), workspace, map[string]mekugiHistory{call: history}); err != nil {
			t.Fatal(err)
		}
	}
	file, temporary, created := filepath.Join(workspace, "file.txt"), filepath.Join(workspace, "temporary.txt"), filepath.Join(workspace, "created.txt")
	publish("main", "/root", "rewrite", file, file, "old\nkeep\n", "draft\nextra\nkeep\n")
	publish("child", "/root/worker", "final", file, file, "draft\nextra\nkeep\n", "final\nkeep\n")
	publish("main", "/root", "create-temporary", "", temporary, "", "one\ntwo\n")
	publish("child", "/root/worker", "delete-temporary", temporary, "", "one\ntwo\n", "")
	publish("child", "/root/worker", "create-final", "", created, "", "new\nfile\n")
	publish("main", "/root", "scratch", "", filepath.Join(t.TempDir(), "scratch.txt"), "", strings.Repeat("scratch\n", 100))
	publish("unrelated", "/root/other", "unrelated", "", filepath.Join(workspace, "unrelated.txt"), "", "unrelated\n")

	u := newAppServerSessionTestUI(t, workspace)
	u.agents.agents = []activityPaneAgent{{Name: "/root"}, {Name: "/root/worker"}}
	u.shell.focus = 3
	u.shell.diff.store = store
	scope := liveDiffScope{Workspaces: map[string]map[string]bool{workspace: {"main": true, "child": true}}}
	refresh := func() {
		t.Helper()
		if _, err := u.shell.diff.applyEvent(t.Context(), liveDiffEvent{Kind: "scope", Scope: &scope, Resync: true}); err != nil {
			t.Fatal(err)
		}
	}
	refresh()

	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer slave.Close()
	frames := make(chan []byte)
	stop, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		var pending []byte
		buf := make([]byte, 8192)
		for {
			n, err := master.Read(buf)
			pending = append(pending, buf[:n]...)
			for {
				end := bytes.Index(pending, []byte("\x1b[?2026l"))
				if end < 0 {
					break
				}
				end += len("\x1b[?2026l")
				select {
				case frames <- bytes.Clone(pending[:end]):
				case <-stop:
					return
				}
				pending = pending[end:]
			}
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() { close(stop); master.Close(); <-stopped })
	screen := vt.NewEmulator(160, 32)
	defer screen.Close()
	paint := func(width int) string {
		t.Helper()
		if err := pty.Setsize(master, &pty.Winsize{Cols: uint16(width), Rows: 32}); err != nil {
			t.Fatal(err)
		}
		screen.Resize(width, 32)
		if err := u.paint(slave, width, 32); err != nil {
			t.Fatal(err)
		}
		select {
		case frame := <-frames:
			if _, err := screen.Write(frame); err != nil {
				t.Fatal(err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("net stats PTY frame timeout")
		}
		return screen.String()
	}
	for _, width := range []int{160, 80, 120} {
		frame := paint(width)
		if !strings.Contains(frame, "+3 -1") || strings.Contains(frame, "+7 -5") {
			t.Fatalf("%d-column frame misreports composed outcome:\n%s", width, frame)
		}
		if width == 160 && (!strings.Contains(frame, "+4 -1") || !strings.Contains(frame, "+3 -4")) {
			t.Fatalf("cumulative agent counts are missing:\n%s", frame)
		}
	}

	// A new store reader and controller projection can restore the outcome
	// without any live parent, command execution, or workspace source files.
	u.shell.diff.store = &mekugiReplayStore{directory: store.directory}
	u.shell.diff.resetScope()
	refresh()
	u.shell.diff.view.FilterCaller("/root/worker")
	refresh()
	if frame := paint(160); !strings.Contains(frame, "+3 -1") {
		t.Fatalf("replay or caller filter changed overall outcome:\n%s", frame)
	}
	u.shell.diff.resetScope()
	scope.Workspaces[workspace] = map[string]bool{"new-thread": true}
	refresh()
	if frame := paint(160); strings.Contains(frame, "+3 -1") || strings.Contains(frame, "+4 -1") || strings.Contains(frame, "+3 -4") {
		t.Fatalf("new thread borrowed old outcome or activity:\n%s", frame)
	}
}
