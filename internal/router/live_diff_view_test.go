package router

import (
	"context"
	"github.com/yusing/mekugi/internal/livediff"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
	"github.com/yusing/mekugi"
)

func TestLiveDiffRenderAllFiles(t *testing.T) {
	workspace := t.TempDir()
	var files []liveDiffFile
	for _, name := range []string{"first", "second"} {
		path := filepath.Join(workspace, name+".txt")
		diff := "--- /dev/null\n+++ " + strconv.Quote(path) + "\n@@ -0,0 +1 @@\n+" + name + " content\n"
		files = append(files, liveDiffFile{Path: path, Chunks: []liveDiffChunk{{
			Status: "Applied",
			Review: mekugi.ReviewFile{AfterPath: path, Diff: diff},
		}}})
	}
	for focusFile := range files {
		render, err := new(liveDiffRenderer).Render(t.Context(), livediff.TerminalTheme, files, workspace, 90, focusFile, files[focusFile].Chunks[0])
		if err != nil {
			t.Fatal(err)
		}
		if len(render.Starts) != 2 || render.Starts[0] != 0 || render.Starts[1] <= 0 {
			t.Fatalf("missing file boundaries: %v", render.Starts)
		}
		for i, name := range []string{"first", "second"} {
			end := len(render.Lines)
			if i+1 < len(files) {
				end = render.Starts[i+1]
			}
			text := ansi.Strip(strings.Join(render.Lines[render.Starts[i]:end], "\n"))
			if !strings.Contains(text, name+".txt") || !strings.Contains(text, name+" content") {
				t.Fatalf("file %d not rendered in its own section: %q", i, text)
			}
			if i == focusFile && (render.FocusOffset < render.Starts[i] || render.FocusOffset >= end) {
				t.Fatalf("same line number in another file stole focus: %+v", render)
			}
		}
	}
}

func TestLiveDiffFollowEmptyLatestFile(t *testing.T) {
	workspace := t.TempDir()
	first := filepath.Join(workspace, "first.txt")
	second := filepath.Join(workspace, "second.txt")
	diff := "--- /dev/null\n+++ " + strconv.Quote(first) + "\n@@ -0,0 +1,40 @@\n" + strings.Repeat("+first content\n", 40)
	files := []liveDiffFile{
		{Path: first, Chunks: []liveDiffChunk{{Status: "Applied",
			Review: mekugi.ReviewFile{AfterPath: first, Diff: diff}}}},
		{Path: second}, // The latest file was flushed or fully reverted.
	}
	render, err := new(liveDiffRenderer).Render(t.Context(), livediff.TerminalTheme, files, workspace, 90, 1, liveDiffChunk{})
	if err != nil {
		t.Fatal(err)
	}
	if render.FocusOffset != render.Starts[1] {
		t.Fatalf("empty latest file lost focus: offset=%d starts=%v", render.FocusOffset, render.Starts)
	}
	const rows = 18
	if len(render.Lines) <= rows {
		t.Fatal("fixture must require scrolling")
	}
	offset := min(render.FocusOffset, len(render.Lines)-rows)
	viewport := ansi.Strip(strings.Join(render.Lines[offset:offset+rows], "\n"))
	if strings.Contains(viewport, "second.txt") || strings.Contains(viewport, "No unreviewed changes") {
		t.Fatalf("reverted file remained Visible: %q", viewport)
	}
}

func TestLiveDiffScrollAcrossFiles(t *testing.T) {
	view := liveDiffView{
		Files:  []liveDiffFile{{Path: "first"}, {Path: "second"}},
		Scroll: make(map[string]int),
	}
	render := liveDiffRender{Lines: make([]string, 20), Starts: []int{0, 10}}
	for _, tc := range []struct {
		offset, file, local int
	}{
		{9, 0, 9}, {10, 1, 0}, {13, 1, 3}, {9, 0, 9}, {-1, 0, 0}, {25, 1, 9},
	} {
		view.ScrollTo(render, tc.offset)
		if view.Selected != tc.file || view.Scroll[view.Files[tc.file].Key()] != tc.local {
			t.Fatalf("scroll %d: selected=%d positions=%v", tc.offset, view.Selected, view.Scroll)
		}
	}
	if view.Scroll["first"] != 0 {
		t.Fatal("scrolling another file lost the first file's saved position")
	}
}

// Exercise the native renderer through the real terminal consumer, with
// two populated files in a single capture. Both must appear in the same frame.
func TestLiveDiffTerminalShowsMultiFileCapture(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	workspace := t.TempDir()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	type capturedEdit struct{ path, after string }
	publish := func(call string, edits []capturedEdit) {
		t.Helper()
		files := make([]mekugi.ReviewFile, 0, len(edits))
		for _, edit := range edits {
			path := filepath.Join(workspace, edit.path)
			before, err := os.ReadFile(path)
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			beforePath := path
			if os.IsNotExist(err) {
				beforePath = ""
			}
			files = append(files, mekugi.RenderReviewFile(beforePath, path, string(before), edit.after))
		}
		id, err := store.reserveChange(t.Context(), workspace, "thread", call)
		if err != nil {
			t.Fatal(err)
		}
		history := mekugiHistory{ToolName: applyPatchToolName, ChangeID: id, CorrelationID: call, Applied: true, ReviewFiles: files}
		if err := store.put(t.Context(), workspace, map[string]mekugiHistory{call: history}); err != nil {
			t.Fatal(err)
		}
	}
	longSuffix := strings.Repeat("x", 160)
	for _, name := range []string{"first.txt", "second.txt"} {
		if err := os.WriteFile(filepath.Join(workspace, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	publish("populate-both", []capturedEdit{
		{"first.txt", "Temporary file one." + longSuffix + "\nStatus: created\n"},
		{"second.txt", "Temporary file two.\nStatus: created\n"},
	})

	for name, content := range map[string]string{
		"first.txt":  "Temporary file one." + longSuffix + "\nStatus: created\n",
		"second.txt": "Temporary file two.\nStatus: created\n",
	} {
		if err := os.WriteFile(filepath.Join(workspace, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLiveDiffTerminalProcess$")
	connection := liveDiffTestSession(t, store, workspace)
	cmd.Env = append(os.Environ(), "MEKUGI_LIVE_DIFF_TEST_CHILD=1",
		"MEKUGI_LIVE_DIFF_WORKSPACE="+workspace, "MEKUGI_LIVE_DIFF_REPLAY="+store.directory, "MEKUGI_LIVE_DIFF_SESSION="+connection)
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 44, Cols: 100})
	if err != nil {
		t.Fatal(err)
	}
	defer terminal.Close()
	done := make(chan struct{})
	var waitErr error
	go func() { waitErr = cmd.Wait(); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	chunks := make(chan string, 64)
	go func() {
		defer close(chunks)
		var buf [8192]byte
		for {
			n, err := terminal.Read(buf[:])
			if n > 0 {
				select {
				case chunks <- string(buf[:n]):
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	topLine, uiHeader := "", ""
	mouseEnabled := false
	waitFrame := func(check func(string) bool) string {
		t.Helper()
		var pending, lastFrame string
		for {
			select {
			case chunk, open := <-chunks:
				if strings.Contains(pending+chunk, "\x1b[?1000;1006h") {
					mouseEnabled = true
				}
				pending += chunk
				for {
					end := strings.Index(pending, "\x1b[?2026l")
					if end < 0 {
						break
					}
					end += len("\x1b[?2026l")
					frame := pending[:end]
					pending = pending[end:]
					if start := strings.LastIndex(frame, "\x1b[1;1H"); start >= 0 {
						_, uiHeader, _ = strings.Cut(frame[start:], "\x1b[1;1H\x1b[0m\x1b[2K")
						uiHeader, _, _ = strings.Cut(uiHeader, "\x1b[2;1H\x1b[0m\x1b[2K")
						uiHeader = ansi.Strip(uiHeader)
						_, topLine, _ = strings.Cut(frame[start:], "\x1b[2;1H\x1b[0m\x1b[2K")
						topLine, _, _ = strings.Cut(topLine, "\x1b[3;1H")
						topLine = ansi.Strip(topLine)
						frame = ansi.Strip(frame[start:])
						lastFrame = frame
						if check(frame) {
							return frame
						}
					}
				}
				if !open {
					t.Fatalf("viewer exited before expected frame: %q", pending)
				}
			case <-ctx.Done():
				t.Fatalf("waiting for frame: last=%q pending=%q", lastFrame, pending)
			}
		}
	}
	waitFrame(func(frame string) bool { return strings.Contains(frame, "STREAM · v diff") })
	if _, err := terminal.Write([]byte("v")); err != nil {
		t.Fatal(err)
	}
	frame := waitFrame(func(frame string) bool {
		return strings.Contains(frame, "first.txt") && strings.Contains(frame, "v stream")
	})
	if !mouseEnabled {
		t.Fatal("viewer did not enable SGR mouse reporting")
	}
	for _, want := range []string{"first.txt", "second.txt", "Temporary file one.", "Temporary file two."} {
		if !strings.Contains(frame, want) {
			t.Fatalf("multi-file capture omitted %q from the visible frame: %q", want, frame)
		}
	}
	if !strings.Contains(uiHeader, "Files  2/2 · tree") || !strings.Contains(uiHeader, "first.txt") || strings.Contains(uiHeader, "Changes") || strings.Contains(uiHeader, "PATH | row") {
		t.Fatalf("short diff should share the first row with the navigator heading: row 1=%q row 2=%q", uiHeader, topLine)
	}
	if strings.Contains(frame, "Applied · original to latest") || strings.Contains(frame, "Δ /dev/null") {
		t.Fatalf("redundant diff headers remain: %q", frame)
	}

	// Horizontal keys and fragmented wheel reports leave following and source
	// unchanged. An ignored printable key asks for a frame after each sequence.
	for _, report := range []string{
		"\x1b[C", "\x1b[D", "\x1bOC", "\x1bOD", "h", "l",
		"\x1b[<67;50;5M", "\x1b[<66;50;5M",
		"\x1b[<67;fFqr;5M\x1b[<67;50;5m",
	} {
		for _, key := range []byte(report + "z") {
			if _, err := terminal.Write([]byte{key}); err != nil {
				t.Fatal(err)
			}
		}
		frame := waitFrame(func(frame string) bool {
			return strings.Contains(frame, "DIFF · ") || strings.Contains(frame, "STREAM · ")
		})
		if !strings.Contains(frame, "FOLLOW") || !strings.Contains(frame, "Temporary file one.") {
			t.Fatalf("horizontal input %q changed the view: %q", report, frame)
		}
	}

	// Pause below the wrapped source row, then narrow it enough to add a
	// continuation. The anchored status row must remain at the viewport top.
	if _, err := terminal.Write([]byte("g" + strings.Repeat("\x1b[<65;50;10M", 2) + "z")); err != nil {
		t.Fatal(err)
	}
	atStatus := func(frame string) bool {
		return strings.Contains(frame, "PAUSED") && strings.Contains(frame, "Status: created") &&
			!strings.Contains(frame, strings.Repeat("x", 8))
	}
	waitFrame(atStatus)
	if err := pty.Setsize(terminal, &pty.Winsize{Rows: 44, Cols: 90}); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Process.Signal(syscall.SIGWINCH); err != nil {
		t.Fatal(err)
	}
	waitFrame(atStatus)
	if err := pty.Setsize(terminal, &pty.Winsize{Rows: 44, Cols: 100}); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Process.Signal(syscall.SIGWINCH); err != nil {
		t.Fatal(err)
	}
	waitFrame(atStatus)

	if _, err := terminal.Write([]byte("r")); err != nil {
		t.Fatal(err)
	}
	frame = waitFrame(func(frame string) bool { return strings.Contains(frame, "FOLLOW") })
	if strings.Contains(frame, "LATEST UPDATE") || strings.Contains(frame, "●") {
		t.Fatal("startup history was marked as newly observed")
	}
	publish("update-both", []capturedEdit{
		{"first.txt", "Temporary file one." + longSuffix + "\nStatus: updated 界 é\n"},
		{"second.txt", "Temporary file two.\nStatus: updated\n"},
	})

	frame = waitFrame(func(frame string) bool {
		return strings.Contains(frame, "first.txt") && strings.Contains(frame, "second.txt") &&
			!strings.Contains(frame, "●") && strings.Contains(frame, "updated 界 é")
	})
	if strings.Contains(frame, "LATEST UPDATE") {
		t.Fatal("update label is still displayed")
	}
	if _, err := terminal.Write([]byte("n")); err != nil {
		t.Fatal(err)
	}
	waitFrame(func(frame string) bool {
		return strings.Contains(frame, "PAUSED") &&
			strings.Contains(frame, "Temporary file two.") && !strings.Contains(frame, "Temporary file one.")
	})
	if err := os.WriteFile(filepath.Join(workspace, "first.txt"),
		[]byte("Temporary file one."+longSuffix+"\nStatus: updated 界 é\n"), 0600); err != nil {
		t.Fatal(err)
	}
	publish("update-first", []capturedEdit{{"first.txt", "Temporary file one." + longSuffix + "\nStatus: adjusted 界 é\n"}})
	waitFrame(func(frame string) bool {
		return strings.Contains(frame, "PAUSED · new changes available") &&
			!strings.Contains(frame, "LATEST UPDATE")
	})
	if err := pty.Setsize(terminal, &pty.Winsize{Rows: 44, Cols: 90}); err != nil {
		t.Fatal(err)
	}
	waitFrame(func(frame string) bool {
		return strings.Contains(frame, "PAUSED · new changes available")
	})
	if _, err := terminal.Write([]byte("r")); err != nil {
		t.Fatal(err)
	}
	waitFrame(func(frame string) bool {
		return strings.Contains(frame, "FOLLOW") &&
			strings.Contains(frame, "adjusted 界 é") && !strings.Contains(frame, "new changes available")
	})
	if _, err := terminal.Write([]byte("n")); err != nil {
		t.Fatal(err)
	}
	waitFrame(func(frame string) bool {
		return strings.Contains(frame, "PAUSED") && strings.Contains(frame, "Temporary file two.") &&
			!strings.Contains(frame, "new changes available")
	})
	if _, err := terminal.Write([]byte("f")); err != nil {
		t.Fatal(err)
	}
	waitFrame(func(frame string) bool {
		return strings.Contains(frame, "PAUSED") && strings.Contains(frame, "adjusted 界 é") &&
			!strings.Contains(frame, "Temporary file two.") &&
			!strings.Contains(frame, "LATEST UPDATE") && !strings.Contains(frame, "new changes available")
	})
	if _, err := terminal.Write([]byte("F")); err != nil {
		t.Fatal(err)
	}
	waitFrame(func(frame string) bool {
		return strings.Contains(frame, "No unreviewed changes") && !strings.Contains(frame, "Temporary file")
	})
	if _, err := terminal.Write([]byte{3}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
		if waitErr != nil {
			t.Fatal(waitErr)
		}
	case <-ctx.Done():
		t.Fatal("viewer failed to quit")
	}
	var tail strings.Builder
	for chunk := range chunks {
		tail.WriteString(chunk)
	}
	if !strings.Contains(tail.String(), "\x1b[?1000;1006l") {
		t.Fatal("viewer did not disable mouse reporting on exit")
	}
}
