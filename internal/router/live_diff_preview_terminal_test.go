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
		// The preview boundary remains fixed as streamed content grows.
		const previewTop = 16
		if !strings.Contains(liveDiffFrameRow(frame, previewTop), "STREAMING SCRIPT") {
			t.Fatalf("preview moved from the fixed 7:3 split: %q", frame)
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
	ui.write(t, "\x1b[<65;2;18M")
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
	if !strings.Contains(liveDiffFrameRow(frame, 9), "STREAMING SCRIPT") {
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
	if !strings.Contains(liveDiffFrameRow(restored, 11), "80│+new") {
		t.Fatalf("preview dismissal left unused rows below the captured diff: %q", restored)
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
		shellFrame := ui.frame(t, func(frame string) bool {
			return strings.Contains(frame, "STREAMING SCRIPT") && strings.Contains(ansi.Strip(frame), "FUNCTIONS_SHELL_TIP")
		})
		if !strings.Contains(liveDiffFrameRow(shellFrame, 16), "STREAMING SCRIPT") {
			t.Fatal("standalone shell simulation did not use the 7:3 split")
		}
		ui.frame(t, func(frame string) bool {
			return strings.Contains(frame, "STREAMING PREVIEW") && strings.Contains(ansi.Strip(frame), "lifecycle.go")
		})
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
	ui.frame(t, func(frame string) bool { return strings.Contains(frame, "STREAMING SCRIPT") })
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
	// Fourteen captured rows while preview is visible: the tip fills the last row.
	if !strings.Contains(liveDiffFrameRow(frame, 15), "FINAL_CHANGED_ROW") {
		t.Fatalf("split viewport left unused rows below the final change: %q", frame)
	}
	broker.publishPreview(liveDiffPreview{ID: preview.ID}, true)
	frame = ui.frame(t, func(frame string) bool { return !strings.Contains(frame, "STREAMING") })
	if !strings.Contains(liveDiffFrameRow(frame, 21), "FINAL_CHANGED_ROW") {
		t.Fatalf("preview dismissal did not fill the restored pane: %q", frame)
	}
	ui.height = 12
	if err := pty.Setsize(ui.pty, &pty.Winsize{Rows: 12, Cols: 60}); err != nil {
		t.Fatal(err)
	}
	frame = ui.frame(t, func(frame string) bool { return strings.Contains(liveDiffFrameRow(frame, 11), "FINAL_CHANGED_ROW") })
	ui.quit(t)
}

func TestLiveDiffTerminalPreviewKeepsFixedSplitWithoutRecentring(t *testing.T) {
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
	ui.frame(t, func(frame string) bool { return strings.Contains(frame, "FOLLOW") })
	for _, size := range []int{1, 10, 1000} {
		preview := previewViewFixture("sizing", size)
		preview.Workspace = workspace
		broker.publishPreview(preview, false)
		frame := ui.frame(t, func(frame string) bool {
			return strings.Contains(ansi.Strip(frame), fmt.Sprintf("stream_%04d", size))
		})
		if !strings.Contains(liveDiffFrameRow(frame, 41), "STREAMING SCRIPT") {
			t.Fatalf("preview moved from the fixed 7:3 split: %q", frame)
		}
		if !strings.Contains(liveDiffFrameRow(frame, 40), "80│+new") {
			t.Fatalf("fixed preview split lost the followed captured tip: %q", frame)
		}
	}
	// A paused viewport must not recenter when the preview releases its rows.
	ui.write(t, "k")
	paused := ui.frame(t, func(frame string) bool { return strings.Contains(frame, "PAUSED") })
	top := liveDiffFrameRow(paused, 2)
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

func TestLiveDiffTerminalStandaloneShellStream(t *testing.T) {
	calls := 0
	transform, proxy, _, workspace := newMekugiTestTransform(t)
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	connection, broker, _ := liveDiffTestBroker(t, store, liveDiffScope{
		Workspaces: map[string]map[string]bool{workspace: {transform.threadID: true, "thread": true}},
	})
	proxy.autoLiveDiff = &autoLiveDiff{events: broker, requested: true}
	proxy.autoLiveDiff.enabled.Store(true)
	ui := startLiveDiffTerminal(t, workspace, store.directory, connection, 22)
	ui.frame(t, func(frame string) bool { return strings.Contains(frame, "FOLLOW") })
	liveDiffTestChange(t, store, workspace, transform.threadID, "captured.go", true)
	ui.frame(t, func(frame string) bool { return strings.Contains(frame, "captured.go") })
	transform.commentaryAuthor, transform.subagentTurn = "/root/editor", true
	// Exercise provider input routing, not a synthetic broker preview.
	_, err = transform.TransformSSE(mustTestJSON(t, map[string]any{
		"type": "response.output_item.added",
		"item": map[string]any{"type": "custom_tool_call", "id": "shell-item", "call_id": "shell-call",
			"name": "shell", "input": "", "status": "in_progress"},
	}))
	if err != nil {
		t.Fatal(err)
	}

	for _, rows := range []int{1, 100, 500} {
		input := strings.Repeat("echo body\n", rows) + fmt.Sprintf("rg 'SHELL_TIP_%d' file.go | head -10\n", rows)
		_, err := transform.TransformSSE(mustTestJSON(t, map[string]any{
			"type": "response.custom_tool_call_input.delta", "item_id": "shell-item", "delta": input,
		}))
		if err != nil {
			t.Fatal(err)
		}
		frame := ui.frame(t, func(frame string) bool {
			return strings.Contains(ansi.Strip(frame), fmt.Sprintf("SHELL_TIP_%d", rows))
		})
		for _, command := range []string{"rg", "head"} {
			if !strings.Contains(frame, liveDiffTerminalTheme.foreground(chroma.NameFunction)+command) {
				t.Fatalf("shell command remains plain in terminal: %q", frame)
			}
		}
		if !strings.Contains(liveDiffFrameRow(frame, 16), "/root/editor") {
			t.Fatalf("provider stream lost caller attribution: %q", frame)
		}
		if !strings.Contains(liveDiffFrameRow(frame, 16), "STREAMING SCRIPT") {
			t.Fatalf("shell preview did not use 7:3 layout: %q", frame)
		}
	}
	ui.height = 12
	if err := pty.Setsize(ui.pty, &pty.Winsize{Rows: 12, Cols: 60}); err != nil {
		t.Fatal(err)
	}
	frame := ui.frame(t, func(frame string) bool {
		return strings.Contains(frame, "\x1b[12;1H") && strings.Contains(frame, "SHELL_TIP_500")
	})
	if !strings.Contains(liveDiffFrameRow(frame, 9), "STREAMING SCRIPT") {
		t.Fatal("resized shell preview lost its 7:3 layout")
	}
	if calls != 0 {
		t.Fatal("streaming shell invoked executable translation")
	}

	transform.Close()
	ui.frame(t, func(frame string) bool { return strings.Contains(frame, "STREAMING COMPLETE") })
	ui.frame(t, func(frame string) bool { return !strings.Contains(frame, "STREAMING") })
	ui.quit(t)
}

func TestLiveDiffTerminalEmptyPreviewUsesAvailableBody(t *testing.T) {
	for _, tool := range []string{"shell"} {
		t.Run(tool, func(t *testing.T) {
			workspace := t.TempDir()
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
			input, tip, colored := "new stream.go\ntype <<PATCH\npackage main\n"+strings.Repeat("// context\n", 30)+"var tip = 42\n", "var tip", liveDiffDarkTheme.foreground(chroma.KeywordDeclaration)+"var"
			if tool == "shell" {
				input, tip, colored = "#!python3\n"+strings.Repeat("# context\n", 30)+"return 42\n", "return 42", liveDiffDarkTheme.foreground(chroma.Keyword)+"return"

			}
			worker.appendDelta(input)
			frame := ui.frame(t, func(frame string) bool { return strings.Contains(ansi.Strip(frame), tip) })
			if !strings.Contains(liveDiffFrameRow(frame, 2), "STREAMING") ||
				strings.Contains(frame, "Waiting for captured") || !strings.Contains(frame, colored) {
				t.Fatalf("empty %s preview wasted space or lost syntax: %q", tool, frame)
			}
			// A full-height preview still follows after resizing and returns to
			// the ordinary split/overlay when the first capture arrives.
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
			frame = ui.frame(t, func(frame string) bool { return strings.Contains(frame, "captured.go") })
			if strings.Contains(liveDiffFrameRow(frame, 2), "STREAMING") {
				t.Fatal("capture did not regain a viewport")
			}
			// Flushing the captured view returns every body row to the stream.
			ui.write(t, "F")
			frame = ui.frame(t, func(frame string) bool { return strings.Contains(liveDiffFrameRow(frame, 2), "STREAMING") })
			if !strings.Contains(frame, colored) {
				t.Fatal("flushing removed stream highlighting")
			}
			worker.stop()
			ui.frame(t, func(frame string) bool { return strings.Contains(frame, "STREAMING COMPLETE") })
			ui.frame(t, func(frame string) bool { return !strings.Contains(frame, "STREAMING") })
			ui.quit(t)
		})
	}
}

func TestLiveDiffTerminalConcurrentCallers(t *testing.T) {
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
	ui.frame(t, func(frame string) bool { return strings.Contains(frame, "FOLLOW") })
	ui.write(t, "g")
	paused := ui.frame(t, func(frame string) bool { return strings.Contains(frame, "PAUSED") })
	top := liveDiffFrameRow(paused, 2)
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
	if !strings.Contains(frame, "/root · first") || !strings.Contains(frame, "/root/editor · second") ||
		liveDiffFrameRow(frame, 2) != top {
		t.Fatalf("concurrent frame lost attribution or moved captured viewport: %q", frame)
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
	if strings.Index(frame, "/root · first") > strings.Index(frame, "/root/editor · second") {
		t.Fatal("concurrent delta reordered the cards")
	}
	broker.publishPreview(first, true)
	ui.frame(t, func(frame string) bool {
		return strings.Contains(frame, "first · STREAMING COMPLETE") && strings.Contains(ansi.Strip(frame), "stream_0200")
	})
	ui.frame(t, func(frame string) bool {
		return !strings.Contains(frame, "first ·") && strings.Contains(ansi.Strip(frame), "stream_0200")
	})
	ui.height = 12
	if err := pty.Setsize(ui.pty, &pty.Winsize{Rows: 12, Cols: 70}); err != nil {
		t.Fatal(err)
	}
	ui.frame(t, func(frame string) bool {
		return strings.Contains(frame, "\x1b[12;1H") && strings.Contains(ansi.Strip(frame), "stream_0200")
	})
	broker.publishPreview(second, true)
	ui.frame(t, func(frame string) bool { return !strings.Contains(frame, "STREAMING") })
	ui.quit(t)
}

func TestLiveDiffTerminalShellHpatchDiff(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "file.txt")
	if err := os.WriteFile(path, []byte("old\n"), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	connection, broker, _ := liveDiffTestBroker(t, store, liveDiffScope{
		Workspaces: map[string]map[string]bool{workspace: {"thread": true}},
	})
	ui := startLiveDiffTerminal(t, workspace, store.directory, connection, 22)
	ui.frame(t, func(frame string) bool { return strings.Contains(frame, "FOLLOW") })
	worker := startLiveDiffPreview(t.Context(), broker, workspace, "thread")
	defer func() { worker.stop(); <-worker.done }()
	// Exercise the shell decoder's no-space heredoc form as it appears in the
	// provider stream, not only the direct decoder fixture.
	worker.appendDelta("hpatch<<'EDIT'\nin file.txt\ntype \"old\" \"new")
	frame := ui.frame(t, func(frame string) bool {
		text := ansi.Strip(frame)
		return strings.Contains(text, "STREAMING PREVIEW") && strings.Contains(text, "+new")
	})
	if strings.Contains(ansi.Strip(frame), "hpatch <<") || !strings.Contains(ansi.Strip(frame), "-old") {
		t.Fatalf("expected a diff, not shell source: %q", frame)
	}
	worker.appendDelta("er")
	ui.frame(t, func(frame string) bool { return strings.Contains(ansi.Strip(frame), "+newer") })
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "old\n" {
		t.Fatalf("preview applied an edit: %q, %v", content, err)
	}
	worker.appendDelta("\"\nin file.txt\ntype \"target that does not exist\" \"rejected\"\nEDIT\n")
	frame = ui.frame(t, func(frame string) bool {
		text := ansi.Strip(frame)
		return strings.Contains(text, "STREAMING PREVIEW: last valid diff; current edit unavailable") &&
			strings.Contains(text, "+newer")
	})
	text := ansi.Strip(frame)
	if strings.Contains(text, "hpatch<<") || !strings.Contains(text, "-old") || !strings.Contains(text, "+newer") {
		t.Fatalf("rejected hpatch suffix replaced the last valid diff with shell source: %q", frame)
	}
	worker.stop()
	ui.frame(t, func(frame string) bool { return strings.Contains(frame, "STREAMING COMPLETE") })
	ui.frame(t, func(frame string) bool { return !strings.Contains(frame, "STREAMING") })
	ui.quit(t)
}
