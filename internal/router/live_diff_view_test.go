package router

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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
		files = append(files, liveDiffFile{path: path, chunks: []liveDiffChunk{{
			status: "Applied", diff: diff,
			review: mekugi.ReviewFile{AfterPath: path, Diff: diff},
		}}})
	}
	for _, sideBySide := range []bool{false, true} {
		t.Run(strconv.FormatBool(sideBySide), func(t *testing.T) {
			delta := liveDiffConfiguredDelta(t, "side-by-side = "+strconv.FormatBool(sideBySide))
			for focusFile := range files {
				render, err := renderLiveDiff(t.Context(), files, delta, workspace, 90, focusFile, files[focusFile].chunks[0])
				if err != nil {
					t.Fatal(err)
				}
				if len(render.starts) != 2 || render.starts[0] != 0 || render.starts[1] <= 0 {
					t.Fatalf("missing file boundaries: %v", render.starts)
				}
				for i, name := range []string{"first", "second"} {
					end := len(render.lines)
					if i+1 < len(files) {
						end = render.starts[i+1]
					}
					text := ansi.Strip(strings.Join(render.lines[render.starts[i]:end], "\n"))
					if !strings.Contains(text, name+".txt") || !strings.Contains(text, name+" content") {
						t.Fatalf("file %d not rendered in its own section: %q", i, text)
					}
					if i == focusFile && (render.focusOffset < render.starts[i] || render.focusOffset >= end) {
						t.Fatalf("same line number in another file stole focus: %+v", render)
					}
				}
			}
		})
	}
}

func TestLiveDiffFollowEmptyLatestFile(t *testing.T) {
	delta, err := exec.LookPath("delta")
	if err != nil {
		t.Skip("delta is not installed")
	}
	workspace := t.TempDir()
	first := filepath.Join(workspace, "first.txt")
	second := filepath.Join(workspace, "second.txt")
	diff := "--- /dev/null\n+++ " + strconv.Quote(first) + "\n@@ -0,0 +1,40 @@\n" + strings.Repeat("+first content\n", 40)
	files := []liveDiffFile{
		{path: first, chunks: []liveDiffChunk{{status: "Applied", diff: diff,
			review: mekugi.ReviewFile{AfterPath: first, Diff: diff}}}},
		{path: second}, // The latest file was flushed or fully reverted.
	}
	render, err := renderLiveDiff(t.Context(), files, delta, workspace, 90, 1, liveDiffChunk{})
	if err != nil {
		t.Fatal(err)
	}
	if render.focusOffset != render.starts[1] {
		t.Fatalf("empty latest file lost focus: offset=%d starts=%v", render.focusOffset, render.starts)
	}
	const rows = 18
	if len(render.lines) <= rows {
		t.Fatal("fixture must require scrolling")
	}
	offset := min(render.focusOffset, len(render.lines)-rows)
	viewport := ansi.Strip(strings.Join(render.lines[offset:offset+rows], "\n"))
	if !strings.Contains(viewport, "second.txt") || !strings.Contains(viewport, "No unreviewed changes") {
		t.Fatalf("following hid the latest file's empty state: %q", viewport)
	}
}

func TestLiveDiffScrollAcrossFiles(t *testing.T) {
	view := liveDiffView{
		files:  []liveDiffFile{{path: "first"}, {path: "second"}},
		scroll: make(map[string]int),
	}
	render := liveDiffRender{lines: make([]string, 20), starts: []int{0, 10}}
	for _, tc := range []struct {
		offset, file, local int
	}{
		{9, 0, 9}, {10, 1, 0}, {13, 1, 3}, {9, 0, 9}, {-1, 0, 0}, {25, 1, 9},
	} {
		view.scrollTo(render, tc.offset)
		if view.selected != tc.file || view.scroll[view.files[tc.file].key()] != tc.local {
			t.Fatalf("scroll %d: selected=%d positions=%v", tc.offset, view.selected, view.scroll)
		}
	}
	if view.scroll["first"] != 0 {
		t.Fatal("scrolling another file lost the first file's saved position")
	}
}

// Reproduce the screenshot through the real delta and terminal consumer, with
// two created files in a single capture. Both must appear in the same frame.
func TestLiveDiffTerminalShowsMultiFileCapture(t *testing.T) {
	if _, err := exec.LookPath("delta"); err != nil {
		t.Skip("delta is not installed")
	}
	workspace := t.TempDir()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	publish := func(call, script string) {
		t.Helper()
		result, err := mekugi.TranslateForHostAt(t.Context(), workspace, script, "")
		if err != nil {
			t.Fatal(err)
		}
		id, err := store.reserveChange(t.Context(), workspace, "thread", call)
		if err != nil {
			t.Fatal(err)
		}
		history := mekugiHistory{ChangeID: id, CorrelationID: call, Applied: true, ReviewFiles: result.ReviewFiles}
		if err := store.put(t.Context(), workspace, map[string]mekugiHistory{call: history}); err != nil {
			t.Fatal(err)
		}
	}
	publish("create-both",
		"new first.txt\ntype \"Temporary file one.\\nStatus: created\\n\"\n"+
			"new second.txt\ntype \"Temporary file two.\\nStatus: created\\n\"\n")
	for name, content := range map[string]string{
		"first.txt":  "Temporary file one.\nStatus: created\n",
		"second.txt": "Temporary file two.\nStatus: created\n",
	} {
		if err := os.WriteFile(filepath.Join(workspace, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLiveDiffTerminalProcess$")
	cmd.Env = append(os.Environ(), "MEKUGI_LIVE_DIFF_TEST_CHILD=1",
		"MEKUGI_LIVE_DIFF_WORKSPACE="+workspace, "MEKUGI_LIVE_DIFF_REPLAY="+store.directory)
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
	waitFrame := func(check func(string) bool) string {
		t.Helper()
		var pending, lastFrame string
		for {
			select {
			case chunk, open := <-chunks:
				pending += chunk
				for {
					end := strings.Index(pending, "q quit\x1b[0m")
					if end < 0 {
						break
					}
					end += len("q quit\x1b[0m")
					frame := pending[:end]
					pending = pending[end:]
					if start := strings.LastIndex(frame, "\x1b[1;1H"); start >= 0 {
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
	frame := waitFrame(func(frame string) bool { return strings.Contains(frame, "FOLLOW") })
	for _, want := range []string{"first.txt", "second.txt", "Temporary file one.", "Temporary file two."} {
		if !strings.Contains(frame, want) {
			t.Fatalf("multi-file capture omitted %q from the visible frame: %q", want, frame)
		}
	}
	if strings.Contains(frame, "LATEST UPDATE") || strings.Contains(frame, "▎") {
		t.Fatal("startup history was marked as newly observed")
	}
	publish("update-both",
		"in first.txt\ntype \"Status: created\" \"Status: updated 界 é\"\n"+
			"in second.txt\ntype \"Status: created\" \"Status: updated\"\n")
	waitFrame(func(frame string) bool {
		return strings.Contains(frame, "first.txt · LATEST UPDATE") &&
			strings.Contains(frame, "second.txt · LATEST UPDATE") &&
			strings.Contains(frame, "▎") && strings.Contains(frame, "updated 界 é")
	})
	if _, err := terminal.Write([]byte("n")); err != nil {
		t.Fatal(err)
	}
	waitFrame(func(frame string) bool {
		return strings.HasPrefix(frame, "2/2") && strings.Contains(frame, "PAUSED") &&
			strings.Contains(frame, "Temporary file two.") && !strings.Contains(frame, "Temporary file one.")
	})
	if err := os.WriteFile(filepath.Join(workspace, "first.txt"),
		[]byte("Temporary file one.\nStatus: updated 界 é\n"), 0600); err != nil {
		t.Fatal(err)
	}
	publish("update-first", "in first.txt\ntype \"Status: updated 界 é\" \"Status: adjusted 界 é\"\n")
	waitFrame(func(frame string) bool {
		return strings.HasPrefix(frame, "2/2") && strings.Contains(frame, "PAUSED · new changes available") &&
			!strings.Contains(frame, "LATEST UPDATE")
	})
	if err := pty.Setsize(terminal, &pty.Winsize{Rows: 44, Cols: 90}); err != nil {
		t.Fatal(err)
	}
	waitFrame(func(frame string) bool {
		return strings.HasPrefix(frame, "2/2") && strings.Contains(frame, "PAUSED · new changes available")
	})
	if _, err := terminal.Write([]byte("r")); err != nil {
		t.Fatal(err)
	}
	waitFrame(func(frame string) bool {
		return strings.HasPrefix(frame, "1/2") && strings.Contains(frame, "FOLLOW") &&
			strings.Contains(frame, "adjusted 界 é") && !strings.Contains(frame, "new changes available")
	})
	if _, err := terminal.Write([]byte("n")); err != nil {
		t.Fatal(err)
	}
	waitFrame(func(frame string) bool {
		return strings.HasPrefix(frame, "2/2") && strings.Contains(frame, "PAUSED") &&
			!strings.Contains(frame, "new changes available")
	})
	if _, err := terminal.Write([]byte("pf")); err != nil {
		t.Fatal(err)
	}
	waitFrame(func(frame string) bool {
		return strings.Contains(frame, "No unreviewed changes") &&
			!strings.Contains(frame, "Temporary file one.") && strings.Contains(frame, "Temporary file two.") &&
			!strings.Contains(frame, "LATEST UPDATE") && !strings.Contains(frame, "new changes available")
	})
	if _, err := terminal.Write([]byte("F")); err != nil {
		t.Fatal(err)
	}
	waitFrame(func(frame string) bool {
		return strings.Contains(frame, "No unreviewed changes") && !strings.Contains(frame, "Temporary file")
	})
	if _, err := terminal.Write([]byte("q")); err != nil {
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
}
