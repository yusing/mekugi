package router

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
	"golang.org/x/term"
)

func TestTerminalGeometry(t *testing.T) {
	for _, width := range []int{1, 60, 99, 100, 180, 300} {
		for _, height := range []int{2, 10, 24, 60} {
			for focus := range 3 {
				l := terminalGeometry(width, height, 999, -10, 0, focus, true, true)
				for _, r := range []terminalRect{l.codex, l.diff, l.agents} {
					if r.w < 0 || r.h < 0 || r.x+r.w > width || r.y+r.h > height-1 {
						t.Fatalf("%dx%d: %+v", width, height, l)
					}
				}
				if width >= 100 && height >= 13 {
					if l.codex.w < 30 || l.diff.w < 40 || l.diff.h < 4 || l.agents.h < 4 {
						t.Fatalf("unusable split: %+v", l)
					}
				}
			}
		}
	}
}

func TestPaneScrollUnified(t *testing.T) {
	for _, key := range []byte{'j', 'k', ' ', 'b', 'g', 'G', 'r'} {
		next, follow, ok := paneScroll(key, 50, 10, 100)
		if !ok {
			t.Fatal("missing scroll binding")
		}
		v := newLiveActivityView()
		v.following = false
		v.offset = 50
		v.feedRows = 10
		v.feedLines = 100
		v.handleKey("", key)
		if v.offset != next || v.following != follow {
			t.Fatalf("agents %c: %d %v, expected %d %v", key, v.offset, v.following, next, follow)
		}
	}
	v := newLiveActivityView()
	v.feedRows = 10
	v.feedLines = 100
	v.handleMouse('k', 2, 2)
	if v.offset != 87 || v.following {
		t.Fatalf("wheel did not pause/scroll: %+v", v)
	}
	for _, seq := range []string{"\x1b[H", "\x1b[1~", "\x1bOH"} {
		var escape string
		for _, b := range []byte(seq) {
			escape, _ = v.handleKey(escape, b)
		}
		if v.offset != 0 {
			t.Fatalf("Home %q: %d", seq, v.offset)
		}
	}
	var escape string
	for _, b := range []byte("\x1b[F") {
		escape, _ = v.handleKey(escape, b)
	}
	if v.offset != 90 || v.following {
		t.Fatal("End must scroll to bottom and stay paused")
	}

}

func TestTerminalUIRoutingAndResize(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "input")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	diff := newLiveDiffTerminalController(nil, "", file)
	defer diff.close()
	u := &terminalUI{master: file, diff: diff, agents: newLiveActivityView(), width: 240, height: 40, side: true, activityOpen: true}
	u.layout = terminalGeometry(240, 40, 100, 20, 0, 0, true, true)
	send := func(s string) {
		t.Helper()
		for _, b := range []byte(s) {
			if err := u.key(b); err != nil {
				t.Fatal(err)
			}
		}
	}
	send("hello\x02" + "2")
	if u.focus != 1 {
		t.Fatal("focus shortcut")
	}
	send("\x02\x1b[D")
	if u.split != 117 {
		t.Fatalf("split = %d", u.split)
	}
	diff.diffMode = true
	send("\x02]")
	if diff.navigation.columns == 0 {
		t.Fatal("file nav did not resize")
	}
	send("\x1b[<0;101;5M\x1b[<32;110;5M\x1b[<0;110;5m")
	if u.split != 109 || u.drag != 0 {
		t.Fatalf("drag: %d %d", u.split, u.drag)
	}
	u.agents.feedRows = 5
	u.agents.feedLines = 20
	send("\x1b[<64;150;25M")
	if u.agents.offset != 12 || u.agents.following {
		t.Fatal("wheel not routed to hovered agents")
	}
	send("\x02" + "1\x1b[200~paste\x02" + "3\x1b[201~")
	if u.focus != 0 {
		t.Fatal("paste interpreted as layout shortcut")
	}
	u.focus = 2
	send("\x1b]52;c;Zg==\x1b\\")
	if u.focus != 2 {
		t.Fatal("terminal reply changed pane focus")
	}
	data, err := os.ReadFile(file.Name())
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello\x1b[200~paste\x023\x1b[201~\x1b]52;c;Zg==\x1b\\" {
		t.Fatalf("Codex input changed: %q", data)
	}
}

// This child acts like a full-screen terminal client, not a mock frame renderer.
func TestTerminalUIChild(t *testing.T) {
	if os.Getenv("MEKUGI_UI_CHILD") != "1" {
		return
	}
	old, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		os.Exit(2)
	}
	defer term.Restore(int(os.Stdin.Fd()), old)
	if os.Getenv("MEKUGI_UI_INLINE") == "1" {
		for i := range 80 {
			fmt.Printf("history_%03d\r\n", i)
		}
		fmt.Print("Codex client ready")
	} else {
		fmt.Print("\x1b[?1049h\x1b[2J\x1b[HCodex client ready")
	}
	var b [1]byte
	for {
		if _, err := os.Stdin.Read(b[:]); err != nil {
			return
		}
		switch b[0] {
		case 'x':
			return
		case 'c':
			fmt.Print("\x1b]52;c;Y29w")
			time.Sleep(time.Millisecond)
			fmt.Print("eQ==\x1b\\")
		case 's':
			w, h, _ := term.GetSize(int(os.Stdout.Fd()))
			fmt.Printf("\x1b[2;1HPTY size %d x %d", w, h)
		}
	}
}

func TestTerminalUIRenderedPTYAndLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	outer, terminal, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer outer.Close()
	defer terminal.Close()
	if err := pty.Setsize(outer, &pty.Winsize{Cols: 220, Rows: 40}); err != nil {
		t.Fatal(err)
	}
	before, err := term.GetState(int(terminal.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	auto, stop := newAutoLiveDiff(ctx, store.directory)
	defer stop()
	activity := newSubagentActivity()
	activity.attachPane(newActivityPane(ctx, auto.requestActivity))
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestTerminalUIChild$")
	cmd.Env = append(os.Environ(), "MEKUGI_UI_CHILD=1")
	wait, err := startTerminalUI(ctx, cmd, terminal, terminal, auto, store, activity)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- wait() }()
	defer func() { cancel(); outer.Close() }()
	frames := make(chan []byte, 32)
	go func() {
		defer close(frames)
		buf := make([]byte, 65536)
		for {
			n, e := outer.Read(buf)
			if n > 0 {
				select {
				case frames <- bytes.Clone(buf[:n]):
				case <-ctx.Done():
					return
				}
			}
			if e != nil {
				return
			}
		}
	}()
	screen := vt.NewEmulator(220, 40)
	defer screen.Close()
	await := func(needle string) {
		t.Helper()
		for {
			if strings.Contains(screen.String(), needle) {
				return
			}
			select {
			case data, ok := <-frames:
				if !ok {
					t.Fatalf("terminal closed waiting for %q", needle)
				}
				if _, err := screen.Write(data); err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatalf("missing %q in frame:\n%s", needle, screen.String())
			}
		}
	}
	await("Codex client ready")
	workspace := t.TempDir()
	auto.observe(workspace, "root", codexTurnMetadata{RequestKind: "turn"})
	auto.requestLaunch(workspace, "root")
	await("Live input")
	io.WriteString(outer, "s")
	await("PTY size 110 x 39")
	auto.events.publishPreview(liveDiffPreview{ID: "preview", Workspace: workspace, Thread: "root", Input: "first line\nsecond line", Status: "running"}, false)
	await("second line")
	io.WriteString(outer, "\x02\x1b[D\x02"+"1")
	for screen.CellAt(107, 0) == nil || screen.CellAt(107, 0).Content != "│" {
		select {
		case data := <-frames:
			screen.Write(data)
		case <-ctx.Done():
			t.Fatal("split resize did not render")
		}
	}
	io.WriteString(outer, "s")
	await("PTY size 107 x 39")
	activity.observe("root", "", "/root", false)
	activity.observe("worker", "root", "/root/worker", true)
	activity.collect("worker", "notice", "reply", "Agent delivery independent of root turns")
	await("Agent delivery independent of root turns")
	io.WriteString(outer, "\x02"+"3\x03")
	await("CODEX ·")
	io.WriteString(outer, "x")
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("UI did not join Codex")
	}
	after, err := term.GetState(int(terminal.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	if *before != *after {
		t.Fatal("terminal mode not restored")
	}
}

func TestTerminalUIPasteIsVerbatim(t *testing.T) {
	for _, payload := range []string{"hello\x1b]unterminated", "hello\x1b[", "\x02\x03\x1b\x1b[23;unterminated", "\x1b]" + strings.Repeat("x", 1024)} {
		for focus := range 3 {
			t.Run(fmt.Sprintf("focus%d/%q", focus, payload[:min(20, len(payload))]), func(t *testing.T) {
				file, err := os.CreateTemp(t.TempDir(), "input")
				if err != nil {
					t.Fatal(err)
				}
				defer file.Close()
				diff := newLiveDiffTerminalController(nil, "", file)
				defer diff.close()
				u := &terminalUI{master: file, diff: diff, agents: newLiveActivityView(), focus: focus, prefix: true}
				input := "\x1b[200~" + payload + "\x1b[201~"
				for _, b := range []byte(input) {
					if err := u.key(b); err != nil {
						t.Fatal(err)
					}
				}
				if u.paste || u.prefix || u.sequence != "" || u.hostReply != nil || u.focus != focus {
					t.Fatal("paste changed focus or left parser in a capture state")
				}
				if err := u.key('z'); err != nil {
					t.Fatal(err)
				}
				got, err := os.ReadFile(file.Name())
				if err != nil {
					t.Fatal(err)
				}
				want := ""
				if focus == 0 {
					want = input + "z"
				}
				if string(got) != want {
					t.Fatalf("input changed: got %q want %q", got, want)
				}
			})
		}
	}
}
