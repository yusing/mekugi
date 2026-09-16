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
// interval/debounce, and following must show each edited row, not stale content.
func TestLiveDiffTerminalRapidUpdates(t *testing.T) {
	workspace := t.TempDir()
	directory := filepath.Join(t.TempDir(), "not-yet", "replay")
	store := &mekugiReplayStore{directory: directory}
	publish := func(call string, reviews []mekugi.ReviewFile, applied bool) mekugiHistory {
		t.Helper()
		id, err := store.reserveChange(t.Context(), workspace, "thread", call)
		if err != nil {
			t.Fatal(err)
		}
		history := mekugiHistory{ChangeID: id, CorrelationID: call, Applied: applied, ReviewFiles: reviews}
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
	pending := ""
	waitFrame := func(want string) string {
		t.Helper()
		for {
			for {
				const endMarker = "q quit\x1b[0m"
				end := strings.Index(pending, endMarker)
				if end < 0 {
					break
				}
				end += len(endMarker)
				frame := pending[:end]
				pending = pending[end:]
				if strings.Contains(ansi.Strip(frame), want) {
					return frame
				}
			}
			select {
			case chunk, open := <-chunks:
				if !open {
					t.Fatalf("viewer exited waiting for %q: %q", want, pending)
				}
				pending += chunk
			case <-ctx.Done():
				t.Fatalf("waiting for %q: %q", want, pending)
			}
		}
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
	waitFrame("Waiting for captured")
	livePublisher := store.liveDiff
	store, err = openMekugiReplayStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	store.liveDiff = livePublisher
	publish("initial", initial, true)
	waitFrame("+original")
	start := time.Now()
	for i, edit := range []struct{ file, line int }{{0, 18}, {3, 1}, {2, 20}, {1, 10}, {4, 2}, {0, 3}} {
		marker := fmt.Sprintf("LATEST%d", i)
		chunk := liveDiffHighlightChunk("", paths[edit.file],
			fmt.Sprintf("@@ -%d +%d @@\n-original\n+%s\n", edit.line, edit.line, marker), true)
		publish(fmt.Sprintf("update%d", i), []mekugi.ReviewFile{chunk.review}, true)
		frame := waitFrame("+" + marker)
		if got := rowText(frame, 5); !strings.Contains(got, "+"+marker) {
			t.Fatalf("latest change is not centered: %q", got)
		}
		if !strings.Contains(frame, "FOLLOW") || strings.Contains(frame, "Unable to combine") {
			t.Fatalf("update %d lost follow or composition: %q", i, frame)
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

	// Resizing a following pane must keep its target inside the smaller body.
	for _, height := range []uint16{5, 3, 9} {
		if err := pty.Setsize(terminal, &pty.Winsize{Rows: height, Cols: 100}); err != nil {
			t.Fatal(err)
		}
		frame := waitFrame("+LATEST5")
		if !strings.Contains(frame, fmt.Sprintf("\x1b[%d;1H", height)) || !strings.Contains(frame, "FOLLOW") {
			t.Fatalf("resize to %d lost target or footer: %q", height, frame)
		}
	}

	if _, err := terminal.Write([]byte("g")); err != nil {
		t.Fatal(err)
	}
	frame := waitFrame("PAUSED")
	if header, heading := rowText(frame, 1), rowText(frame, 2); !strings.HasPrefix(header, "▎ 1/5") || !strings.HasPrefix(heading, "▎ 1/5") {
		t.Fatalf("sticky and in-view headings are shifted: header=%q heading=%q", header, heading)
	}
	chunk := liveDiffHighlightChunk("", paths[4], "@@ -19 +19 @@\n-original\n+PREPARED19\n", false)
	history := publish("pending", []mekugi.ReviewFile{chunk.review}, false)
	waitFrame("PAUSED · new changes available")
	if _, err := terminal.Write([]byte("r")); err != nil {
		t.Fatal(err)
	}
	frame = waitFrame("+PREPARED19")
	if !strings.Contains(ansi.Strip(frame), "application unconfirmed") {
		t.Fatalf("following hid the prepared caption: %q", frame)
	}
	if got := rowText(frame, 5); !strings.Contains(got, "+PREPARED19") {
		t.Fatalf("prepared change is not centered: %q", got)
	}
	history.confirmed = true
	if err := store.confirmChanges(t.Context(), workspace, map[string]mekugiHistory{"pending": history}); err != nil {
		t.Fatal(err)
	}
	frame = waitFrame("+PREPARED19")
	if strings.Contains(ansi.Strip(frame), "application unconfirmed") {
		t.Fatalf("receipt did not refresh prepared content: %q", frame)
	}
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
