//go:build unix

package router

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
	model "github.com/yusing/mekugi/internal/session"
)

func TestNativeRuntimePTYStreamingScrollingResizeAndPrompt(t *testing.T) {
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { slave.Close() })
	if err := pty.Setsize(master, &pty.Winsize{Cols: 90, Rows: 24}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	frames := make(chan []byte, 16)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		var pending []byte
		buf := make([]byte, 8192)
		for {
			n, err := master.Read(buf)
			pending = append(pending, buf[:n]...)
			for {
				at := bytes.Index(pending, []byte("\x1b[?2026l"))
				if at < 0 {
					break
				}
				at += len("\x1b[?2026l")
				select {
				case frames <- bytes.Clone(pending[:at]):
				case <-ctx.Done():
					return
				}
				pending = pending[at:]
			}
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() { cancel(); slave.Close(); master.Close(); <-readerDone })
	observations, binding, _ := observationHTTPFixture(t)
	if err := observations.owner.bind(ctx, binding); err != nil {
		t.Fatal(err)
	}
	f := &runtimeTestClient{events: make(chan model.Event, 16)}
	done := make(chan error, 1)
	go func() { done <- RunNativeSession(ctx, f, "Claude Code", binding.Workspace, slave, slave, observations) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("terminal did not restore on cancellation")
		}
	})
	screen := vt.NewEmulator(90, 24)
	defer screen.Close()
	waitFrame := func(contains string) string {
		t.Helper()
		timer := time.NewTimer(3 * time.Second)
		defer timer.Stop()
		for {
			select {
			case frame := <-frames:
				// These startup queries have no visual rows. This fixture
				// supplies no theme responses, so don't ask the emulator to
				// write replies to its synchronous input pipe.
				frame = bytes.ReplaceAll(frame, []byte("\x1b]10;?\x1b\\"), nil)
				frame = bytes.ReplaceAll(frame, []byte("\x1b]11;?\x1b\\"), nil)
				if _, err := screen.Write(frame); err != nil {
					t.Fatal(err)
				}
				if strings.Contains(screen.String(), contains) {
					return screen.String()
				}
			case <-timer.C:
				t.Fatalf("missing %q in frame:\n%s", contains, screen.String())
				return ""
			}
		}
	}
	f.events <- model.Event{Kind: "ready"}
	waitFrame("Ready")
	f.events <- model.Event{Kind: "message", ID: "m", Role: "Claude", Text: "Stream before completion"}
	waitFrame("Stream before completion")
	var long strings.Builder
	for i := range 40 {
		fmt.Fprintf(&long, "row-%02d\n\n", i)
	}
	f.events <- model.Event{Kind: "message", ID: "m", Role: "Claude", Text: long.String()}
	waitFrame("row-39")
	if _, err := master.Write([]byte("\x1b[5~")); err != nil {
		t.Fatal(err)
	}
	frame := waitFrame("row-30")
	if strings.Contains(frame, "row-39") {
		t.Fatal("page-up did not leave bottom")
	}
	if err := pty.Setsize(master, &pty.Winsize{Cols: 42, Rows: 18}); err != nil {
		t.Fatal(err)
	}
	screen.Resize(42, 18)
	// The same live dock and Diff pane receive a proposal before any native
	// result. Resize/focus occur while that argument stream is still open.
	f.events <- model.Event{Kind: "edit", ID: "edit", Role: "Write", Edit: &model.Edit{Path: "preview.txt", Content: "PTY_PREVIEW_PREFIX", Partial: true}}
	if _, err := master.Write([]byte("\x02" + "2")); err != nil {
		t.Fatal(err)
	}
	waitFrame("PTY_PREVIEW_PREFIX")
	f.events <- model.Event{Kind: "tool_result", ID: "edit", Failed: true, Text: "denied"}
	waitFrame("preview")
	// Saved bytes arrive independently after a failed native tool. Capture never
	// promotes the proposal, and the existing pane reconciles through its mailbox.
	call := ObservationCall{Binding: binding, ID: "captured", Tool: "Write", Input: `{}`, Paths: []string{"captured.txt"}}
	if err := observations.owner.before(ctx, call); err != nil {
		t.Fatal(err)
	}
	f.events <- model.Event{Kind: "edit", ID: call.ID, Role: "Write", Edit: &model.Edit{Path: "captured.txt", Content: "NOT_THE_CAPTURE", Partial: true}}
	waitFrame("NOT_THE_CAPTURE")
	nativeObservationWrite(t, filepath.Join(binding.Workspace, "captured.txt"), "ACTUAL_CAPTURED_BYTES\n")
	if _, err := observations.owner.after(ctx, call, ObservationTerminal{Status: "failed"}); err != nil {
		t.Fatal(err)
	}
	f.events <- model.Event{Kind: "tool_result", ID: call.ID, Failed: true, Text: "partial write failure"}
	f.events <- model.Event{Kind: "done", Failed: true, Text: "native tool failed"}
	frame = waitFrame("ACTUAL_CAPTURED_BYTES")
	if strings.Contains(frame, "NOT_THE_CAPTURE") {
		t.Fatal("proposed bytes were promoted into saved capture")
	}
	// User selection remains live until explicitly switched back, including resize.
	if _, err := master.Write([]byte("v")); err != nil {
		t.Fatal(err)
	}
	waitFrame("live proposals")
	if err := pty.Setsize(master, &pty.Winsize{Cols: 90, Rows: 24}); err != nil {
		t.Fatal(err)
	}
	screen.Resize(90, 24)
	waitFrame("live proposals")
	if _, err := master.Write([]byte("v")); err != nil {
		t.Fatal(err)
	}
	waitFrame("ACTUAL_CAPTURED_BYTES")

	if _, err := master.Write([]byte("\x02" + "1")); err != nil {
		t.Fatal(err)
	}
	f.events <- model.Event{Kind: "prompt", Prompt: &model.Prompt{ID: "p", Tool: "Bash", Description: "Native permission"}}
	waitFrame("Native permission")
	if _, err := master.Write([]byte("2\r")); err != nil {
		t.Fatal(err)
	}
	waitFrame("Turn ended")
	f.events <- model.Event{Kind: "tool", ID: "t", Role: "Read", Text: "observed tool"}
	if _, err := master.Write([]byte("\x02" + "3")); err != nil {
		t.Fatal(err)
	}
	waitFrame("observed tool")
	cancel()
	// The cleanup joins the UI before reading fake-client state under -race.
}
