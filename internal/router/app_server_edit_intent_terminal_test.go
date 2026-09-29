package router

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
	"github.com/yusing/mekugi"
)

func TestAppServerEditIntentTerminalFrames(t *testing.T) {
	const width, height = 160, 40
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer slave.Close()
	if err := pty.Setsize(master, &pty.Winsize{Rows: height, Cols: width}); err != nil {
		t.Fatal(err)
	}
	frames := make(chan string, 3)
	go func() {
		var pending []byte
		buffer := make([]byte, 8192)
		for {
			n, err := master.Read(buffer)
			if err != nil {
				return
			}
			pending = append(pending, buffer[:n]...)
			for {
				end := bytes.Index(pending, []byte("\x1b[?2026l"))
				if end < 0 {
					break
				}
				end += len("\x1b[?2026l")
				frames <- string(pending[:end])
				pending = pending[end:]
			}
		}
	}()
	screen := vt.NewEmulator(width, height)
	defer screen.Close()
	nextFrame := func() string {
		t.Helper()
		if err := t.Context().Err(); err != nil {
			t.Fatal(err)
		}
		select {
		case frame := <-frames:
			if _, err := screen.Write([]byte(frame)); err != nil {
				t.Fatal(err)
			}
			return screen.String()
		case <-time.After(2 * time.Second):
			t.Fatal("terminal frame did not complete")
			return ""
		}
	}
	workspace := t.TempDir()
	u := newAppServerSessionTestUI(t, workspace)
	command := "cat >> a.go <<'EOF'\nPRIVATE_CAT_BODY\nEOF\npython3 - <<'PY'\np='b.go';open(p,'w').write('PRIVATE_PY_BODY')\nPY\ngo test ./internal/router"
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "t", "item": map[string]any{
		"id": "cmd", "type": "commandExecution", "command": command, "status": "inProgress"}})
	finishPacing(u.view, u.agents)
	if err := u.paint(slave, width, height); err != nil {
		t.Fatal(err)
	}
	frame := nextFrame()
	for _, want := range []string{"Edit", "a.go", "b.go", "requested", "Running", "go test"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("requested edit missing %q from terminal frame:\n%s", want, frame)
		}
	}
	if strings.Contains(frame, "PRIVATE_CAT_BODY") || strings.Contains(frame, "PRIVATE_PY_BODY") {
		t.Fatalf("terminal displayed edit source:\n%s", frame)
	}
	history := mekugiHistory{ExecutingThread: "main", CorrelationID: "cmd\x00exec", ChangeID: "c1",
		ExecOutcome: &execOutcome{Status: execStatusCompleted, Coverage: execCoverageExact, Labels: []string{"cat", "python3"}},
		ReviewFiles: []mekugi.ReviewFile{mekugi.RenderReviewFile("a.go", "a.go", "old\n", "new\n")}}
	for i := range 8 {
		path := fmt.Sprintf("PRIVATE_UNKNOWN_FILE_%02d.go", i)
		history.ReviewFiles = append(history.ReviewFiles, mekugi.ReviewFile{BeforePath: path, AfterPath: path, Incomplete: "unavailable"})
	}
	receipt := capturedEditActivity(workspace, history)
	if receipt == nil {
		t.Fatal("test receipt was not created")
	}
	data := newLiveDiffData()
	data.order = []string{"receipt"}
	data.attempts["receipt"] = liveDiffAttempt{receipt: receipt}
	u.shell.diff.data = data
	u.applyCapturedEdits()
	if err := u.paint(slave, width, height); err != nil {
		t.Fatal(err)
	}
	frame = nextFrame()
	for _, want := range []string{"Edit", "a.go", "Capture", "incomplete", "8 paths"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("confirmed receipt missing %q from terminal frame:\n%s", want, frame)
		}
	}
	if strings.Contains(frame, "requested") || strings.Contains(frame, "PRIVATE_UNKNOWN_FILE") ||
		strings.Contains(frame, "PRIVATE_CAT_BODY") || strings.Contains(frame, "PRIVATE_PY_BODY") {
		t.Fatalf("terminal retained requested/source/unknown file detail after receipt:\n%s", frame)
	}
	// A failed command with only unavailable evidence must still show its
	// exit status after the Capture notice replaces its original Run row.
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": map[string]any{
		"id": "cmd", "type": "commandExecution", "command": command, "exitCode": 2}})
	history.ReviewFiles = history.ReviewFiles[1:]
	data.attempts["receipt"] = liveDiffAttempt{receipt: capturedEditActivity(workspace, history)}
	u.applyCapturedEdits()
	if err := u.paint(slave, width, height); err != nil {
		t.Fatal(err)
	}
	frame = nextFrame()
	if !strings.Contains(frame, "Capture") || !strings.Contains(frame, "shell batch · exit 2") || strings.Count(frame, "exit 2") != 1 || strings.Contains(frame, "PRIVATE_UNKNOWN_FILE") {
		t.Fatalf("incomplete-only receipt hid command failure:\n%s", frame)
	}
}
