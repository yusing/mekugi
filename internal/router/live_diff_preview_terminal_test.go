package router

import (
	"context"
	"fmt"
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
	ui.frame(t, func(frame string) bool { return strings.Contains(frame, "FOLLOW") })
	ui.write(t, "g")
	paused := ui.frame(t, func(frame string) bool { return strings.Contains(frame, "PAUSED") })
	header, top := liveDiffFrameRow(paused, 1), liveDiffFrameRow(paused, 2)

	publish := func(id string, rows int) {
		preview := previewViewFixture(id, rows)
		preview.Workspace = workspace
		broker.publishPreview(preview, false)
	}
	waitTip := func(rows int) string {
		t.Helper()
		return ui.frame(t, func(frame string) bool {
			return strings.Contains(ansi.Strip(frame), fmt.Sprintf("+stream_%04d", rows))
		})
	}
	start := time.Now()
	for _, rows := range []int{10, 50, 200, 500, 1000} {
		publish("one", rows)
		frame := waitTip(rows)
		if liveDiffFrameRow(frame, 1) != header || liveDiffFrameRow(frame, 2) != top {
			t.Fatal("streaming moved the paused captured-diff viewport")
		}
		// Short previews fit their content; long previews cap at 70%.
		previewTop := 22 - min(14, rows+1)
		if !strings.Contains(liveDiffFrameRow(frame, previewTop), "STREAMING PREVIEW") {
			t.Fatalf("preview does not use content-aware height: %q", frame)
		}
		for row := 2; row < previewTop; row++ {
			if strings.Contains(liveDiffFrameRow(frame, row), "stream_") {
				t.Fatal("preview rows leaked into captured diff")
			}
		}
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("five streaming-to-visible-frame round trips lagged: %s", elapsed)
	}
	// The preview is unscrollable; wheel reports inside it must be ignored.
	ui.write(t, "\x1b[<65;2;12M")
	publish("one", 1001)
	frame := waitTip(1001)
	if liveDiffFrameRow(frame, 1) != header {
		t.Fatal("wheel input in streaming region moved captured diff")
	}
	// Actual diff scrolling works while the stream keeps following its own tip.
	ui.write(t, "j")
	scrolled := ui.frame(t, func(frame string) bool { return liveDiffFrameRow(frame, 1) != header })
	scrolledHeader := liveDiffFrameRow(scrolled, 1)
	publish("one", 1002)
	frame = waitTip(1002)
	if liveDiffFrameRow(frame, 1) != scrolledHeader {
		t.Fatal("preview update reset captured-diff scroll")
	}
	ui.write(t, "r")
	ui.frame(t, func(frame string) bool { return strings.Contains(frame, "FOLLOW") })
	publish("one", 1200)
	waitTip(1200)

	// Resize still follows rows far outside the original viewport.
	ui.height = 12
	if err := pty.Setsize(ui.pty, &pty.Winsize{Rows: 12, Cols: 60}); err != nil {
		t.Fatal(err)
	}
	frame = ui.frame(t, func(frame string) bool {
		return strings.Contains(frame, "\x1b[12;1H") && strings.Contains(frame, "stream_1200")
	})
	if !strings.Contains(liveDiffFrameRow(frame, 5), "STREAMING PREVIEW") {
		t.Fatal("resize broke the preview height cap")
	}
	// A burst must collapse to its newest frame rather than animate its backlog.
	for rows := 1201; rows <= 1400; rows++ {
		publish("one", rows)
	}
	waitTip(1400)
	broker.publishPreview(liveDiffPreview{ID: "one"}, true)
	completed := ui.frame(t, func(frame string) bool { return strings.Contains(frame, "STREAMING COMPLETE") })
	if !strings.Contains(completed, "stream_1400") {
		t.Fatal("completion lost the last rendered stream")
	}
	restored := ui.frame(t, func(frame string) bool { return !strings.Contains(frame, "STREAMING") })
	if !strings.Contains(liveDiffFrameRow(restored, 7), "80│+new") {
		t.Fatalf("preview dismissal did not center the final captured row: %q", restored)
	}
	// Scroll back from EOF to verify all reclaimed rows are usable.
	ui.write(t, "b")
	ui.frame(t, func(frame string) bool {
		return strings.Contains(frame, "PAUSED") && strings.Contains(liveDiffFrameRow(frame, 10), "│")
	})
	// A new stream interrupted by router coverage loss clears the region.
	publish("two", 20)
	waitTip(20)
	broker.mu.Lock()
	broker.emitLocked(liveDiffEvent{Kind: "coverage", Status: "RECONNECTING: test interruption"})
	broker.mu.Unlock()
	ui.frame(t, func(frame string) bool {
		return strings.Contains(frame, "RECONNECTING") && !strings.Contains(frame, "STREAMING")
	})
	ui.quit(t)
}

func TestLiveDiffSimulationTerminalReplay(t *testing.T) {
	// Two runs of the same user command exercise deterministic replay, isolated
	// cleanup, actual delta projection, and the same terminal UI users receive.
	for range 2 {
		directory := t.TempDir()
		ui := startLiveDiffTerminal(t, "", "", "", 22, "MEKUGI_LIVE_DIFF_SIMULATION_TEST=1", "TMPDIR="+directory)
		ui.frame(t, func(frame string) bool {
			return strings.Contains(frame, "STREAMING PREVIEW") && strings.Contains(ansi.Strip(frame), "/api/")
		})
		ui.write(t, "g")
		ui.frame(t, func(frame string) bool { return strings.Contains(frame, "PAUSED") })
		ui.frame(t, func(frame string) bool { return strings.Contains(frame, "ROUTES_READY") })
		ui.frame(t, func(frame string) bool { return strings.Contains(ansi.Strip(frame), "demo requests") })
		// Exercise real code, not just synthetic repeated rows, at a narrow size.
		if err := pty.Setsize(ui.pty, &pty.Winsize{Rows: 22, Cols: 60}); err != nil {
			t.Fatal(err)
		}
		ui.frame(t, func(frame string) bool { return strings.Contains(ansi.Strip(frame), "response.Code") })
		ui.frame(t, func(frame string) bool {
			return strings.Contains(frame, "STREAMING SCRIPT") && strings.Contains(ansi.Strip(frame), "SHELL_TIP")
		})
		ui.frame(t, func(frame string) bool { return strings.Contains(frame, "lifecycle.go") })
		ui.frame(t, func(frame string) bool { return strings.Contains(ansi.Strip(frame), "without final newline") })
		ui.frame(t, func(frame string) bool { return strings.Contains(frame, "rejected as expected") })
		ui.frame(t, func(frame string) bool { return strings.Contains(ansi.Strip(frame), "INTERRUPTED_TIP") })
		final := ui.frame(t, func(frame string) bool {
			return strings.Contains(frame, "SIMULATION: finished") && !strings.Contains(frame, "STREAMING")
		})
		if !strings.Contains(final, "PAUSED") {
			t.Fatal("simulation altered captured-diff follow state")
		}
		ui.quit(t)
		entries, err := os.ReadDir(directory)
		if err != nil || len(entries) != 0 {
			t.Fatalf("simulation left temporary state: %v %v", entries, err)
		}
	}
}

func TestLiveDiffTerminalPreviewKeepsUsefulFrameAndStreamsMixedInput(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "file.go"), []byte("package old\n"), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	connection, broker, _ := liveDiffTestBroker(t, store, liveDiffScope{
		Workspaces: map[string]map[string]bool{workspace: {"thread": true}},
	})
	ui := startLiveDiffTerminal(t, workspace, store.directory, connection, 22, "COLORFGBG=15;0")
	ui.frame(t, func(frame string) bool { return strings.Contains(frame, "FOLLOW") })
	worker := startLiveDiffPreview(t.Context(), broker, workspace, "thread")
	t.Cleanup(worker.stop)
	worker.appendDelta("in file.go\ntype \"old\" \"main\"\n")
	frame := ui.frame(t, func(frame string) bool { return strings.Contains(ansi.Strip(frame), "+package main") })
	if !strings.Contains(frame, liveDiffDarkTheme.foreground(chroma.KeywordNamespace)+"package") {
		t.Fatalf("terminal preview lacks Go syntax color: %q", frame)
	}
	if strings.Contains(frame, "validated or applied") {
		t.Fatal("redundant disclaimer still rendered")
	}
	// The unfinished target cannot resolve. The old worker erased the good
	// preview and emitted PREVIEW UNAVAILABLE at this point.
	worker.appendDelta("type \"pack")
	time.Sleep(150 * time.Millisecond)
	ui.write(t, "z")
	frame = ui.frame(t, func(frame string) bool {
		return strings.Contains(ansi.Strip(frame), "+package main") || strings.Contains(frame, "UNAVAILABLE")
	})
	if strings.Contains(frame, "UNAVAILABLE") {
		t.Fatalf("unfinished target erased useful source: %q", frame)
	}
	worker.appendDelta("age\" \"package\"\nshell <<SHELL\nprintf 'SHELL_TIP")
	ui.frame(t, func(frame string) bool {
		return strings.Contains(frame, "STREAMING SCRIPT") && strings.Contains(ansi.Strip(frame), "SHELL_TIP")
	})
	worker.appendDelta("'\nSHELL\nnew after.go\ntype <<PATCH\npackage after\n")
	ui.frame(t, func(frame string) bool {
		return strings.Contains(frame, "STREAMING SCRIPT") && strings.Contains(ansi.Strip(frame), "package after")
	})
	if _, err := os.Stat(filepath.Join(workspace, "after.go")); !os.IsNotExist(err) {
		t.Fatal("streamed shell suffix was executed")
	}
	worker.stop()
	ui.frame(t, func(frame string) bool { return strings.Contains(frame, "STREAMING COMPLETE") })
	ui.frame(t, func(frame string) bool { return !strings.Contains(frame, "STREAMING") })
	ui.quit(t)
}

func TestLiveDiffTerminalCentersFinalRowAfterPreview(t *testing.T) {
	workspace := t.TempDir()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	connection, broker, _ := liveDiffTestBroker(t, store, liveDiffScope{
		Workspaces: map[string]map[string]bool{workspace: {"thread": true}},
	})
	ui := startLiveDiffTerminal(t, workspace, store.directory, connection, 22)
	ui.frame(t, func(frame string) bool { return strings.Contains(frame, "FOLLOW") })
	preview := previewViewFixture("creation", 100)
	preview.Workspace = workspace
	broker.publishPreview(preview, false)
	ui.frame(t, func(frame string) bool { return strings.Contains(frame, "STREAMING PREVIEW") })
	id, err := store.reserveChange(t.Context(), workspace, "thread", "create")
	if err != nil {
		t.Fatal(err)
	}
	diff := "@@ -0,0 +1,100 @@\n" + strings.Repeat("+earlier\n", 99) + "+FINAL_CHANGED_ROW\n"
	if err := store.put(t.Context(), workspace, map[string]mekugiHistory{"create": {
		ChangeID: id, CorrelationID: "create", Applied: true,
		ReviewFiles: []mekugi.ReviewFile{{AfterPath: filepath.Join(workspace, "new.go"), Diff: diff}},
	}}); err != nil {
		t.Fatal(err)
	}
	frame := ui.frame(t, func(frame string) bool { return strings.Contains(frame, "FINAL_CHANGED_ROW") })
	// Six captured rows while preview is visible: the tip belongs at row 5.
	if !strings.Contains(liveDiffFrameRow(frame, 5), "FINAL_CHANGED_ROW") {
		t.Fatalf("split viewport did not center final change: %q", frame)
	}
	broker.publishPreview(liveDiffPreview{ID: preview.ID}, true)
	frame = ui.frame(t, func(frame string) bool { return !strings.Contains(frame, "STREAMING") })
	if !strings.Contains(liveDiffFrameRow(frame, 12), "FINAL_CHANGED_ROW") {
		t.Fatalf("preview dismissal did not recenter the followed diff: %q", frame)
	}
	ui.height = 12
	if err := pty.Setsize(ui.pty, &pty.Winsize{Rows: 12, Cols: 60}); err != nil {
		t.Fatal(err)
	}
	frame = ui.frame(t, func(frame string) bool { return strings.Contains(liveDiffFrameRow(frame, 7), "FINAL_CHANGED_ROW") })
	ui.quit(t)
}

func TestLiveDiffTerminalPreviewFillsBottomWithoutRecentring(t *testing.T) {
	workspace := t.TempDir()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	connection, broker, _ := liveDiffTestBroker(t, store, liveDiffScope{
		Workspaces: map[string]map[string]bool{workspace: {"thread": true}},
	})
	liveDiffTestChange(t, store, workspace, "capture", "handler.go", true)
	ui := startLiveDiffTerminal(t, workspace, store.directory, connection, 58)
	initial := ui.frame(t, func(frame string) bool { return strings.Contains(frame, "FOLLOW") })
	top := liveDiffFrameRow(initial, 2)
	for _, size := range []int{1, 10, 1000} {
		preview := previewViewFixture("sizing", size)
		preview.Workspace = workspace
		broker.publishPreview(preview, false)
		frame := ui.frame(t, func(frame string) bool {
			return strings.Contains(ansi.Strip(frame), fmt.Sprintf("stream_%04d", size))
		})
		if !strings.Contains(liveDiffFrameRow(frame, 57), fmt.Sprintf("stream_%04d", size)) {
			t.Fatalf("preview left unused rows below its streaming tip: %q", frame)
		}
		if size < 1000 && liveDiffFrameRow(frame, 2) != top {
			t.Fatalf("short preview shifted the captured viewport: %q", frame)
		}
		top = liveDiffFrameRow(frame, 2)
	}
	// A paused viewport must not recenter when the preview releases its rows.
	ui.write(t, "k")
	paused := ui.frame(t, func(frame string) bool { return strings.Contains(frame, "PAUSED") })
	top = liveDiffFrameRow(paused, 2)
	broker.publishPreview(liveDiffPreview{ID: "sizing"}, true)
	restored := ui.frame(t, func(frame string) bool { return !strings.Contains(frame, "STREAMING") })
	if liveDiffFrameRow(restored, 2) != top {
		t.Fatal("preview dismissal recentered unchanged captured content")
	}
	ui.quit(t)
}

func TestLiveDiffTerminalComposedContextIsNotDuplicated(t *testing.T) {
	workspace := t.TempDir()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	connection, _, _ := liveDiffTestBroker(t, store, liveDiffScope{
		Workspaces: map[string]map[string]bool{workspace: {"thread": true}},
	})
	ui := startLiveDiffTerminal(t, workspace, store.directory, connection, 22)
	ui.frame(t, func(frame string) bool { return strings.Contains(frame, "FOLLOW") })
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
