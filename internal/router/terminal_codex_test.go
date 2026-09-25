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

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
	"golang.org/x/term"
)

func TestCodexClipboardAndScrollback(t *testing.T) {
	var host bytes.Buffer
	u := &terminalUI{codex: vt.NewEmulator(30, 5)}
	defer u.codex.Close()
	u.configureCodex(&host)
	sequence := "\x1b]52;c;Y29weQ==\x1b\\"
	for _, b := range []byte(sequence) {
		u.codex.Write([]byte{b})
	}
	if host.String() != sequence {
		t.Fatalf("clipboard output=%q", host.String())
	}
	for i := range 20 {
		fmt.Fprintf(u.codex, "line_%02d\r\n", i)
	}
	u.scrollCodex(10)
	if !strings.Contains(ansi.Strip(u.codexFrame()), "line_06") || strings.Contains(ansi.Strip(u.codexFrame()), "line_19") {
		t.Fatalf("history frame: %s", u.codexFrame())
	}
	u.scrollCodex(-100)
	if !strings.Contains(ansi.Strip(u.codexFrame()), "line_19") {
		t.Fatal("history did not return to live screen")
	}
	u.codex.Write([]byte("\x1b[?1000h"))
	if len(u.mouseModes) != 1 {
		t.Fatal("Codex mouse capture not tracked")
	}
	u.codex.Write([]byte("\x1b[?1000l"))
	if len(u.mouseModes) != 0 {
		t.Fatal("Codex mouse release not tracked")
	}
}

func TestTerminalUIInlineHistoryAndClipboardPTY(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	outer, terminal, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer outer.Close()
	defer terminal.Close()
	if err := pty.Setsize(outer, &pty.Winsize{Cols: 100, Rows: 12}); err != nil {
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
	cmd.Env = append(os.Environ(), "MEKUGI_UI_CHILD=1", "MEKUGI_UI_INLINE=1")
	wait, err := startTerminalUI(ctx, cmd, terminal, terminal, auto, store, activity)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- wait() }()
	output := make(chan []byte, 32)
	go func() {
		defer close(output)
		buf := make([]byte, 16384)
		for {
			n, err := outer.Read(buf)
			if n > 0 {
				select {
				case output <- bytes.Clone(buf[:n]):
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	screen := vt.NewEmulator(100, 12)
	defer screen.Close()
	var raw strings.Builder
	await := func(match func() bool) {
		t.Helper()
		for !match() {
			select {
			case b, ok := <-output:
				if !ok {
					t.Fatal("PTY closed")
				}
				raw.Write(b)
				screen.Write(b)
			case <-ctx.Done():
				t.Fatalf("waiting for terminal frame:\n%s", screen.String())
			}
		}
	}
	await(func() bool { return strings.Contains(screen.String(), "Codex client ready") })
	io.WriteString(outer, "\x02\x1b[5~")
	await(func() bool { return strings.Contains(screen.String(), "history_060") })
	io.WriteString(outer, "c") // Typing returns to live and asks child for fragmented OSC52.
	clipboard := "\x1b]52;c;Y29weQ==\x1b\\"
	await(func() bool { return strings.Contains(raw.String(), clipboard) })
	if strings.Count(raw.String(), clipboard) != 1 {
		t.Fatal("clipboard request forwarded more than once")
	}
	await(func() bool {
		return strings.Contains(raw.String(), "\x1b]2;⠋ Codex working\x1b\\") && strings.Contains(raw.String(), "\x1b]9;4;3\x1b\\")
	})
	io.WriteString(outer, "x")
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("UI did not exit")
	}
	terminal.Close()
	for b := range output {
		raw.Write(b)
	}
	if !strings.Contains(raw.String(), "history_000") {
		t.Fatal("exit discarded inline history")
	}
}

func TestTerminalUICancelRestoresAndJoinsPTY(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	outer, terminal, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer outer.Close()
	defer terminal.Close()
	if err := pty.Setsize(outer, &pty.Winsize{Cols: 100, Rows: 20}); err != nil {
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
	ready, readDone := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(readDone)
		var seen strings.Builder
		buf := make([]byte, 16384)
		for {
			n, e := outer.Read(buf)
			seen.Write(buf[:n])
			if strings.Contains(seen.String(), "Codex client ready") {
				close(ready)
				_, _ = io.Copy(io.Discard, outer)
				return
			}
			if e != nil {
				return
			}
		}
	}()
	defer func() { terminal.Close(); outer.Close(); <-readDone }()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("no initial frame")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation did not join Codex and terminal readers")
	}
	if cmd.ProcessState == nil {
		t.Fatal("Codex process not reaped")
	}
	after, err := term.GetState(int(terminal.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	if *before != *after {
		t.Fatal("terminal raw mode survived cancellation")
	}
}

func TestCodexHostTitleAndProgressPassThrough(t *testing.T) {
	var host bytes.Buffer
	u := &terminalUI{codex: vt.NewEmulator(30, 5)}
	defer u.codex.Close()
	u.configureCodex(&host)
	for _, payload := range []string{"0;Codex", "2;⠋ Working", "2;Action Required", "9;4;3", "9;4;0"} {
		for _, terminator := range []string{"\a", "\x1b\\"} {
			host.Reset()
			for _, b := range []byte("\x1b]" + payload + terminator) {
				if _, err := u.codex.Write([]byte{b}); err != nil {
					t.Fatal(err)
				}
			}
			if got, want := host.String(), "\x1b]"+payload+"\x1b\\"; got != want {
				t.Fatalf("host signal = %q, want %q", got, want)
			}
		}
	}
}
