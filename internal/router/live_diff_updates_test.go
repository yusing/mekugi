package router

import (
	"context"
	"fmt"
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

// Real terminal acceptance: sequential publications cannot hide behind an
// interval/debounce, and live refresh must show edits to the selected file, not stale content.
func TestLiveDiffTerminalRapidUpdates(t *testing.T) {
	workspace := t.TempDir()
	directory := filepath.Join(t.TempDir(), "not-yet", "replay")
	store := &mekugiReplayStore{directory: directory}
	publish := func(call string, reviews []mekugi.ReviewFile, outcome *execOutcome) mekugiHistory {
		t.Helper()
		id, err := store.reserveChange(t.Context(), workspace, "thread", call)
		if err != nil {
			t.Fatal(err)
		}
		history := mekugiHistory{ChangeID: id, CorrelationID: call, ExecOutcome: outcome, ReviewFiles: reviews}
		if err := store.put(t.Context(), workspace, map[string]mekugiHistory{call: history}); err != nil {
			t.Fatal(err)
		}
		return history
	}
	paths := make([]string, 5)
	var initial []mekugi.ReviewFile
	for i := range paths {
		paths[i] = filepath.Join(workspace, fmt.Sprintf("file%d.tmp", i+1))
		initial = append(initial, mekugi.ReviewFile{AfterPath: paths[i],
			Diff: "--- /dev/null\n+++ " + strconv.Quote(paths[i]) +
				"\n@@ -0,0 +1,20 @@\n" + strings.Repeat("+original\n", 20)})
	}

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLiveDiffTerminalProcess$")
	connection := liveDiffTestSession(t, store, workspace)
	cmd.Env = append(os.Environ(), "MEKUGI_LIVE_DIFF_TEST_CHILD=1",
		"MEKUGI_LIVE_DIFF_WORKSPACE="+workspace, "MEKUGI_LIVE_DIFF_REPLAY="+store.directory, "MEKUGI_LIVE_DIFF_SESSION="+connection)
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 9, Cols: 100})
	if err != nil {
		t.Fatal(err)
	}
	defer terminal.Close()
	done := make(chan struct{})
	var waitErr error
	go func() { waitErr = cmd.Wait(); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	chunks := readPTYChunks(ctx, terminal, 8192, 64)
	var pending synchronizedFrameBuffer
	waitMatchingFrame := func(matches func(string) bool) string {
		t.Helper()
		for {
			for {
				data, ok := pending.Next()
				if !ok {
					break
				}
				frame := string(data)
				if matches(frame) {
					return frame
				}
			}
			select {
			case chunk, open := <-chunks:
				if !open {
					t.Fatalf("viewer exited before expected terminal frame: %q", pending.pending)
				}
				pending.Append(chunk)
			case <-ctx.Done():
				t.Fatalf("waiting for expected terminal frame: %q", pending.pending)
			}
		}
	}
	waitFrame := func(want string) string {
		t.Helper()
		return waitMatchingFrame(func(frame string) bool { return strings.Contains(ansi.Strip(frame), want) })
	}
	rowText := func(frame string, row int) string {
		t.Helper()
		_, text, found := strings.Cut(frame, fmt.Sprintf("\x1b[%d;1H\x1b[0m\x1b[2K", row))
		if !found {
			t.Fatalf("missing screen row %d: %q", row, frame)
		}
		text, _, _ = strings.Cut(text, fmt.Sprintf("\x1b[%d;1H", row+1))
		return ansi.Strip(text)
	}
	waitFrame("STREAM · v diff")
	if _, err := terminal.Write([]byte("v")); err != nil {
		t.Fatal(err)
	}
	livePublisher := store.liveDiff
	store, err = openMekugiReplayStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	store.liveDiff = livePublisher
	publish("initial", initial, nil)
	waitFrame("+original")
	start := time.Now()
	for i, edit := range []struct{ file, line int }{{0, 1}, {0, 1}, {0, 1}, {0, 1}, {0, 1}, {0, 1}} {
		marker := fmt.Sprintf("LATEST%d", i)
		before := "original"
		if i > 0 {
			before = fmt.Sprintf("LATEST%d", i-1)
		}
		chunk := liveDiffHighlightChunk("", paths[edit.file],
			fmt.Sprintf("@@ -%d +%d @@\n-%s\n+%s\n", edit.line, edit.line, before, marker))
		publish(fmt.Sprintf("update%d", i), []mekugi.ReviewFile{chunk.Review}, nil)
		frame := waitFrame("+" + marker)

		if strings.Contains(frame, "FOLLOW") || strings.Contains(frame, "Unable to combine") {
			t.Fatalf("update %d exposed stale follow or lost composition: %q", i, frame)
		}
	}
	elapsed := time.Since(start)
	t.Logf("six publication-to-visible-frame round trips: %s", elapsed)
	// One-second polling takes at least five seconds for six sequential
	// publications. This allows ample filesystem/scheduler headroom while
	// rejecting interval-based updates without fragile per-frame deadlines.
	if elapsed > 3*time.Second {
		t.Fatalf("updates are delayed: six round trips took %s", elapsed)
	}

	// Resizing preserves the chosen file without a follow state.
	for _, height := range []uint16{5, 3, 9} {
		if err := pty.Setsize(terminal, &pty.Winsize{Rows: height, Cols: 100}); err != nil {
			t.Fatal(err)
		}
		frame := waitFrame("DIFF · v stream")
		if !strings.Contains(frame, fmt.Sprintf("\x1b[%d;1H", height)) || strings.Contains(frame, "FOLLOW") {
			t.Fatalf("resize to %d lost target or footer: %q", height, frame)
		}
	}

	if _, err := terminal.Write([]byte("g")); err != nil {
		t.Fatal(err)
	}
	frame := waitFrame("DIFF · v stream")
	if heading := rowText(frame, 1); !strings.Contains(heading, "Files  5/5 · tree") || strings.Contains(heading, "Changes") ||
		strings.Contains(heading, "| row") || !strings.Contains(heading, "▎ 1/5  file1.tmp") {
		t.Fatalf("navigator and first diff heading are shifted: heading=%q", heading)
	}
	chunk := liveDiffHighlightChunk("", paths[4], "@@ -19 +19 @@\n-original\n+PREPARED19\n")
	publish("failed-command-edit", []mekugi.ReviewFile{chunk.Review}, &execOutcome{Status: execStatusFailed, Exit: new(1)})
	waitFrame("DIFF · v stream")
	if _, err := terminal.Write([]byte("/" + filepath.Base(paths[4]) + "\rG")); err != nil {
		t.Fatal(err)
	}
	frame = waitFrame("+PREPARED19")
	if strings.Contains(ansi.Strip(frame), "changes observed") || strings.Contains(ansi.Strip(frame), "application unconfirmed") {
		t.Fatalf("following rendered obsolete change state: %q", frame)
	}
	fix := liveDiffHighlightChunk("", paths[4], "@@ -19 +19 @@\n-PREPARED19\n+FINAL19\n")
	publish("followup-edit", []mekugi.ReviewFile{fix.Review}, nil)
	frame = waitFrame("+FINAL19")
	plain := ansi.Strip(frame)
	for _, stale := range []string{"+PREPARED19", "command failed", "exit 1", "changes observed", "Unable to combine"} {
		if strings.Contains(plain, stale) {
			t.Fatalf("failed command contaminated composed diff with %q: %q", stale, plain)
		}
	}
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
}
