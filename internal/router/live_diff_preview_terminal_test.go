package router

import (
	"context"
	"fmt"
	"github.com/yusing/mekugi/internal/livediff"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alecthomas/chroma/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
	"github.com/yusing/mekugi"
)

// The UI harness consumes complete terminal frames, not publication callbacks.
// It runs the real viewer process against the authenticated router event stream.
type liveDiffTerminalHarness struct {
	ctx     context.Context
	pty     *os.File
	chunks  <-chan string
	height  uint16
	done    chan struct{}
	waitErr error
	pending string
}

func startLiveDiffTerminal(t *testing.T, workspace, replay, connection string, height uint16, extraEnv ...string) *liveDiffTerminalHarness {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLiveDiffTerminalProcess$")
	cmd.Env = append(os.Environ(), "MEKUGI_LIVE_DIFF_TEST_CHILD=1",
		"MEKUGI_LIVE_DIFF_WORKSPACE="+workspace, "MEKUGI_LIVE_DIFF_REPLAY="+replay,
		"MEKUGI_LIVE_DIFF_SESSION="+connection)
	cmd.Env = append(cmd.Env, extraEnv...)
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: height, Cols: 100})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	done := make(chan struct{})
	t.Cleanup(func() { cancel(); terminal.Close(); <-done })
	chunks := make(chan string, 64)
	go func() {
		defer close(chunks)
		var buffer [8192]byte
		for {
			n, err := terminal.Read(buffer[:])
			if n > 0 {
				select {
				case chunks <- string(buffer[:n]):
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	harness := &liveDiffTerminalHarness{ctx: ctx, pty: terminal, chunks: chunks, height: height, done: done}
	go func() { harness.waitErr = cmd.Wait(); close(done) }()
	return harness
}

func (h *liveDiffTerminalHarness) frame(t *testing.T, check func(string) bool) string {
	t.Helper()
	last := ""
	for {
		footer := fmt.Sprintf("\x1b[%d;1H\x1b[0m\x1b[2K", h.height)
		end := -1
		if start := strings.Index(h.pending, footer); start >= 0 {
			if stop := strings.Index(h.pending[start+len(footer):], "\x1b[?2026l"); stop >= 0 {
				end = start + len(footer) + stop + len("\x1b[?2026l")
			}
		}
		if end >= 0 {
			frame := h.pending[:end]
			if strings.Count(frame, "\x1b[?2026h") != 1 ||
				strings.Index(frame, "\x1b[?2026h") > strings.Index(frame, "\x1b[1;1H") {
				t.Fatalf("frame was not synchronized before row clearing: %q", frame)
			}
			h.pending = h.pending[end:]
			last = frame
			if check(frame) {
				return frame
			}
			continue
		}
		select {
		case chunk, open := <-h.chunks:
			if !open {
				t.Fatalf("viewer exited before expected frame: %q", last)
			}
			h.pending += chunk
		case <-h.ctx.Done():
			t.Fatalf("waiting for terminal frame: last=%q pending=%q", last, h.pending)
		}
	}
}

func (h *liveDiffTerminalHarness) write(t *testing.T, input string) {
	t.Helper()
	if _, err := h.pty.WriteString(input); err != nil {
		t.Fatal(err)
	}
}

func (h *liveDiffTerminalHarness) quit(t *testing.T) {
	t.Helper()
	h.write(t, "q")
	select {
	case <-h.done:
		if h.waitErr != nil {
			t.Fatalf("viewer did not exit cleanly: %v", h.waitErr)
		}
	case <-h.ctx.Done():
		t.Fatal("viewer did not stop")
	}
}

func liveDiffFrameRow(frame string, row int) string {
	_, text, _ := strings.Cut(frame, fmt.Sprintf("\x1b[%d;1H\x1b[0m\x1b[2K", row))
	text, _, _ = strings.Cut(text, fmt.Sprintf("\x1b[%d;1H", row+1))
	return ansi.Strip(text)
}

func TestLiveDiffTerminalStreamingRegion(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	connection, broker, _ := liveDiffTestBroker(t, store, liveDiffScope{
		Workspaces: map[string]map[string]bool{workspace: {"thread": true}},
	})
	liveDiffTestChange(t, store, workspace, "one", "captured.go", true)
	ui := startLiveDiffTerminal(t, workspace, store.directory, connection, 22)
	ui.frame(t, func(frame string) bool { return strings.Contains(frame, "STREAM · v diff") })

	preview := previewViewFixture("one", 100)
	preview.Workspace = workspace
	broker.publishPreview(preview, false)
	stream := ui.frame(t, func(frame string) bool {
		return strings.Contains(frame, "STREAMING SCRIPT") && strings.Contains(ansi.Strip(frame), "+stream_0100")
	})
	if strings.Contains(stream, "80│+new") {
		t.Fatal("default stream mode shared the pane with captured diff")
	}

	// Diff navigation and mouse input are ignored while stream mode is active.
	ui.write(t, "g\x1b[<65;2;18M")
	broker.publishTurn(false)
	ui.frame(t, func(frame string) bool {
		return strings.Contains(frame, "v stream") && strings.Contains(ansi.Strip(frame), "80│+new")
	})
	ui.write(t, "g")
	// The diff footer reports the call still streaming in the other mode.
	paused := ui.frame(t, func(frame string) bool { return strings.Contains(frame, "DIFF · v stream (1 live) · PAUSED") })
	pausedHeader := liveDiffFrameRow(paused, 1)

	ui.write(t, "v")
	ui.frame(t, func(frame string) bool { return strings.Contains(frame, "STREAM · v diff") })
	broker.publishPreview(liveDiffPreview{ID: "one"}, true)
	completed := ui.frame(t, func(frame string) bool {
		return strings.Contains(frame, "STREAMING COMPLETE") && strings.Contains(ansi.Strip(frame), "+stream_0100")
	})
	if strings.Contains(completed, "80│+new") {
		t.Fatal("completed stream stopped owning the full pane")
	}

	// Toggling modes preserves captured-diff paused position.
	broker.publishTurn(false)
	resumedDiff := ui.frame(t, func(frame string) bool { return strings.Contains(frame, "DIFF · v stream · PAUSED") })
	if liveDiffFrameRow(resumedDiff, 1) == "" || pausedHeader == "" {
		t.Fatal("turn transition lost captured-diff navigation state")
	}

	// Router coverage loss clears retained stream cards.
	broker.publishTurn(true)
	ui.frame(t, func(frame string) bool { return strings.Contains(frame, "STREAM · v diff") })
	broker.mu.Lock()
	broker.emitLocked(liveDiffEvent{Kind: "coverage", Status: "RECONNECTING: test interruption"})
	broker.mu.Unlock()
	ui.frame(t, func(frame string) bool {
		return strings.Contains(frame, "RECONNECTING") && !strings.Contains(frame, "STREAMING")
	})
	ui.quit(t)
}

func TestLiveDiffTerminalTurnRevisionPreservesManualReconnectChoice(t *testing.T) {
	controller := newLiveDiffTerminalController(nil, "", nil)
	defer controller.close()
	if _, err := controller.applyEvent(t.Context(), liveDiffEvent{Kind: "turn", Status: "active", TurnRevision: 1}); err != nil {
		t.Fatal(err)
	}
	controller.handleKey('v')
	if !controller.diffMode {
		t.Fatal("manual toggle did not select diff mode")
	}
	if _, err := controller.applyEvent(t.Context(), liveDiffEvent{Kind: "turn", Status: "active", TurnRevision: 1}); err != nil {
		t.Fatal(err)
	}
	if !controller.diffMode {
		t.Fatal("replayed turn revision reset manual mode")
	}
	if _, err := controller.applyEvent(t.Context(), liveDiffEvent{Kind: "turn", Status: "active", TurnRevision: 2}); err != nil {
		t.Fatal(err)
	}
	if controller.diffMode {
		t.Fatal("new active turn did not reset manual mode to stream")
	}
}

func TestLiveDiffTerminalComposedContextIsNotDuplicated(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	connection, _, _ := liveDiffTestBroker(t, store, liveDiffScope{
		Workspaces: map[string]map[string]bool{workspace: {"thread": true}},
	})
	ui := startLiveDiffTerminal(t, workspace, store.directory, connection, 22)
	ui.frame(t, func(frame string) bool { return strings.Contains(frame, "STREAM · v diff") })
	ui.write(t, "v")
	id, err := store.reserveChange(t.Context(), workspace, "thread", "nearby")
	if err != nil {
		t.Fatal(err)
	}
	diff := "@@ -1,5 +1,5 @@\n-old first\n+new first\n See the editing guide\n and prerequisites.\n-old second\n+new second\n tail\n"
	path := filepath.Join(workspace, "readme.md")
	if err := store.put(t.Context(), workspace, map[string]mekugiHistory{"nearby": {
		ChangeID: id, CorrelationID: "nearby", Applied: true,
		ReviewFiles: []mekugi.ReviewFile{{BeforePath: path, AfterPath: path, Diff: diff}},
	}}); err != nil {
		t.Fatal(err)
	}
	frame := ui.frame(t, func(frame string) bool { return strings.Contains(ansi.Strip(frame), "new second") })
	for _, text := range []string{"See the editing guide", "and prerequisites."} {
		if strings.Count(ansi.Strip(frame), text) != 1 {
			t.Fatalf("duplicated composed context %q: %q", text, ansi.Strip(frame))
		}
	}
	ui.quit(t)
}

func TestLiveDiffTerminalEmptyPreviewUsesAvailableBody(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	connection, broker, _ := liveDiffTestBroker(t, store, liveDiffScope{
		Workspaces: map[string]map[string]bool{workspace: {"thread": true}},
	})
	ui := startLiveDiffTerminal(t, workspace, store.directory, connection, 22, "COLORFGBG=15;0")
	ui.frame(t, func(frame string) bool { return strings.Contains(frame, "STREAM · v diff") })
	worker := startLiveDiffPreview(t.Context(), broker, workspace, "thread")
	t.Cleanup(worker.stop)
	input := "#!python3\n" + strings.Repeat("# context\n", 30) + "return 42\n"
	tip, colored := "return 42", livediff.DarkTheme.Foreground(chroma.Keyword)+"return"
	worker.appendDelta(input)
	frame := ui.frame(t, func(frame string) bool { return strings.Contains(ansi.Strip(frame), tip) })
	if !strings.Contains(liveDiffFrameRow(frame, 2), "STREAMING") ||
		strings.Contains(frame, "Waiting for captured") || !strings.Contains(frame, colored) {
		t.Fatalf("empty shell preview wasted space or lost syntax: %q", frame)
	}
	// A full-height preview still follows after resizing and stays visible
	// when the first capture arrives.
	ui.height = 12
	if err := pty.Setsize(ui.pty, &pty.Winsize{Rows: 12, Cols: 70}); err != nil {
		t.Fatal(err)
	}
	frame = ui.frame(t, func(frame string) bool {
		return strings.Contains(frame, "\x1b[12;1H") && strings.Contains(ansi.Strip(frame), tip)
	})
	if !strings.Contains(liveDiffFrameRow(frame, 2), "STREAMING") {
		t.Fatal("resize restored an empty split")
	}
	liveDiffTestChange(t, store, workspace, "thread", "captured.go", true)
	frame = ui.frame(t, func(frame string) bool { return strings.Contains(ansi.Strip(frame), tip) })
	if !strings.Contains(liveDiffFrameRow(frame, 2), "STREAMING") {
		t.Fatal("capture displaced the full-pane stream")
	}
	worker.stop()
	ui.frame(t, func(frame string) bool { return strings.Contains(frame, "STREAMING COMPLETE") })
	ui.quit(t)
}

func TestLiveDiffTerminalConcurrentCallers(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	connection, broker, _ := liveDiffTestBroker(t, store, liveDiffScope{
		Workspaces: map[string]map[string]bool{workspace: {"thread": true, "child": true}},
	})
	liveDiffTestChange(t, store, workspace, "capture", "captured.go", true)
	ui := startLiveDiffTerminal(t, workspace, store.directory, connection, 22)
	ui.frame(t, func(frame string) bool { return strings.Contains(frame, "STREAM · v diff") })
	ui.write(t, "v")
	ui.write(t, "g")
	ui.frame(t, func(frame string) bool { return strings.Contains(frame, "PAUSED") })
	ui.write(t, "v")
	first := previewViewFixture("first", 100)
	first.Workspace, first.Caller = workspace, "/root"
	second := previewViewFixture("second", 200)
	second.Workspace, second.Thread, second.Caller = workspace, "child", "/root/editor"
	broker.publishPreview(first, false)
	broker.publishPreview(second, false)
	frame := ui.frame(t, func(frame string) bool {
		text := ansi.Strip(frame)
		return strings.Contains(text, "stream_0100") && strings.Contains(text, "stream_0200")
	})
	if text := ansi.Strip(frame); !strings.Contains(text, "/root · STREAMING SCRIPT") || !strings.Contains(text, "/root/editor · STREAMING SCRIPT") {
		t.Fatalf("concurrent frame lost attribution: %q", frame)
	}
	// Child callers keep the agents pane's color for the same canonical path.
	if !strings.Contains(frame, liveAgentColor("/root/editor")+"/root/editor") || !strings.Contains(frame, "STREAM · v diff") {
		t.Fatalf("concurrent frame lost attribution: %q", frame)
	}
	for i := 101; i <= 150; i++ {
		first = previewViewFixture("first", i)
		first.Workspace, first.Caller = workspace, "/root"
		broker.publishPreview(first, false)
	}
	frame = ui.frame(t, func(frame string) bool {
		text := ansi.Strip(frame)
		return strings.Contains(text, "stream_0150") && strings.Contains(text, "stream_0200")
	})
	if text := ansi.Strip(frame); strings.Index(text, "/root · STREAMING SCRIPT") > strings.Index(text, "/root/editor · STREAMING SCRIPT") {
		t.Fatal("concurrent delta reordered the cards")
	}
	broker.publishPreview(first, true)
	ui.frame(t, func(frame string) bool {
		return strings.Contains(frame, "/root · STREAMING COMPLETE") && strings.Contains(ansi.Strip(frame), "stream_0200")
	})
	ui.height = 12
	if err := pty.Setsize(ui.pty, &pty.Winsize{Rows: 12, Cols: 70}); err != nil {
		t.Fatal(err)
	}
	ui.frame(t, func(frame string) bool {
		return strings.Contains(frame, "\x1b[12;1H") && strings.Contains(ansi.Strip(frame), "stream_0200")
	})
	broker.publishPreview(second, true)
	ui.frame(t, func(frame string) bool { return strings.Count(frame, "STREAMING COMPLETE") == 2 })
	ui.write(t, "v")
	diff := ui.frame(t, func(frame string) bool { return strings.Contains(frame, "PAUSED") })
	if !strings.Contains(diff, "v stream") {
		t.Fatal("stream updates changed the paused diff mode")
	}
	ui.quit(t)
}
