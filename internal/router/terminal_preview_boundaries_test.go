package router

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
	"github.com/yusing/mekugi/internal/livediff"
)

func TestTerminalUIPreviewGuidedPTY(t *testing.T) {
	for _, skip := range []bool{false, true} {
		t.Run(fmt.Sprintf("skip_story=%t", skip), func(t *testing.T) {
			t.Parallel()
			testTerminalUIPreviewGuidedPTY(t, skip)
		})
	}
}

func testTerminalUIPreviewGuidedPTY(t *testing.T, skipStory bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	outer, terminal, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer outer.Close()
	defer terminal.Close()
	if err := pty.Setsize(outer, &pty.Winsize{Cols: 180, Rows: 36}); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		pace := 10 * time.Millisecond
		if skipStory {
			pace = time.Hour
		} // Skipping must interrupt the story timer.
		runPreviewBoundaryUI(t, ctx, terminal, pace)
	}()
	t.Cleanup(func() { cancel(); <-done; outer.Close() })
	chunks := make(chan []byte, 32)
	go func() {
		defer close(chunks)
		buffer := make([]byte, 65536)
		for {
			n, err := outer.Read(buffer)
			if n > 0 {
				select {
				case chunks <- bytes.Clone(buffer[:n]):
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	screen := vt.NewEmulator(180, 36)
	defer screen.Close()
	var pending []byte
	awaitFrame := func(marker string, matches func(string) bool) string {
		t.Helper()
		for {
			if frame := screen.String(); matches(frame) {
				return frame
			}
			const frameEnd = "\x1b[?2026l"
			if end := bytes.Index(pending, []byte(frameEnd)); end >= 0 {
				end += len(frameEnd)
				if _, err := screen.Write(pending[:end]); err != nil {
					t.Fatal(err)
				}
				pending = pending[end:]
				continue
			}
			select {
			case chunk, ok := <-chunks:
				if !ok {
					t.Fatalf("preview exited before %q", marker)
				}
				pending = append(pending, chunk...)
			case <-ctx.Done():
				t.Fatalf("missing %q in frame:\n%s", marker, screen.String())
			}
		}
	}
	await := func(marker string) string {
		t.Helper()
		return awaitFrame(marker, func(frame string) bool { return strings.Contains(frame, marker) })
	}

	if skipStory {
		await("Press s to skip the story")
		if _, err := outer.WriteString("1"); err != nil {
			t.Fatal(err)
		}
		await("CHECKPOINT 1.1 READY")
	} else {
		await("Ready: press Enter to start.")
		await("usage_review")
	}
	await("ACTIVITY")
	await("│+PREVIOUS DIFF")
	for step := 1; step <= 5; step++ {
		if !skipStory || step != 1 {
			if _, err := outer.WriteString("\r"); err != nil {
				t.Fatal(err)
			}
		}
		frame := await(fmt.Sprintf("CHECKPOINT 1.%d READY", step))
		if step >= 2 {
			frame = await("│+# Boundary notes")
		}
		if step == 5 {
			frame = await("│+A partial paragraph")
		}
		if !strings.Contains(frame, "ACTIVITY") || !strings.Contains(frame, "AGENTS") {
			t.Fatalf("boundary checkpoint removed the other panes:\n%s", frame)
		}
		if step == 1 && !strings.Contains(frame, "│+PREVIOUS DIFF") {
			t.Fatalf("header-only checkpoint blanked preceding diff:\n%s", frame)
		}
		if step >= 2 && !strings.Contains(frame, "│+# Boundary notes") {
			t.Fatalf("baseline missing in rendered diff:\n%s", frame)
		}
		if step < 5 && strings.Contains(frame, "│+A partial paragraph") {
			t.Fatalf("partial paragraph leaked at checkpoint %d:\n%s", step, frame)
		}
		if step == 5 && !strings.Contains(frame, "│+A partial paragraph") {
			t.Fatalf("newline did not reveal paragraph:\n%s", frame)
		}
	}
	// Selection, replay, back, and queued skips must work without traversing
	// every checkpoint or dropping keys while the preview is rendering.
	for _, action := range []struct{ keys, marker string }{
		{"3", "CHECKPOINT 3.1 READY"},
		{"\r", "CHECKPOINT 3.2 READY"},
		{"r", "CHECKPOINT 3.1 READY"},
		{"nn", "CHECKPOINT 5.1 READY"},
		{"b", "CHECKPOINT 4.1 READY"},
		{"8", "CHECKPOINT 8.1 READY"},
	} {
		if _, err := outer.WriteString(action.keys); err != nil {
			t.Fatal(err)
		}
		await(action.marker)
	}
	// Exercise every fixture through the live worker and rendered diff pane,
	// not the synchronous projector or text in the EXPECT instructions. Story
	// entry does not affect fixtures, so one subtest covers them.
	layout := terminalGeometry(180, 36, 0, 0, 0, 0, true, true)
	fixtures := previewBoundaryCases()
	if !skipStory {
		fixtures = nil
	}
	for index, fixture := range fixtures {
		for phase, checkpoint := range fixture.Steps {
			key := "\r"
			if phase == 0 {
				key = fmt.Sprint(index + 1)
			}
			if _, err := outer.WriteString(key); err != nil {
				t.Fatal(err)
			}
			marker := fmt.Sprintf("CHECKPOINT %d.%d READY", index+1, phase+1)
			awaitFrame(marker+" rendered expectations", func(frame string) bool {
				if !strings.Contains(frame, marker) {
					return false
				}
				var diff strings.Builder
				for _, line := range strings.Split(frame, "\n")[layout.diff.y : layout.diff.y+layout.diff.h] {
					diff.WriteString(ansi.Cut(line, layout.diff.x, layout.diff.x+layout.diff.w))
					diff.WriteByte('\n')
				}
				for _, want := range checkpoint.Want {
					if !strings.Contains(diff.String(), want) {
						return false
					}
				}
				for _, absent := range checkpoint.Absent {
					if strings.Contains(diff.String(), absent) {
						return false
					}
				}
				return strings.Contains(frame, "ACTIVITY") && strings.Contains(frame, "AGENTS")
			})
		}
	}

	if _, err := outer.WriteString("\x04"); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case <-done:
			return
		case <-chunks:
			// Keep draining the terminal while shutdown flushes its last frames.
		case <-ctx.Done():
			t.Fatal("guided preview did not exit on Ctrl-D")
		}
	}
}

// The stand-in displays guidance and forwards navigation keys. It does not claim
// that a fixture or a visually inspected frame passed an automated check.
func previewBoundaryConsole() {
	messages := os.NewFile(3, "preview-guidance")
	advance := os.NewFile(4, "preview-next")
	done := make(chan struct{})
	go func() { defer close(done); _, _ = io.Copy(os.Stdout, messages) }()
	defer func() { advance.Close(); messages.Close(); <-done }()
	var key [1]byte
	for {
		if _, err := os.Stdin.Read(key[:]); err != nil {
			return
		}
		if key[0] == 4 {
			_, _ = advance.Write([]byte{4})
			return
		}
		if key[0] == '\r' || key[0] == '\n' || strings.ContainsRune("12345678nrbs", rune(key[0])) {
			if _, err := advance.Write(key[:]); err != nil {
				return
			}
		}
	}
}

const previewBoundaryControls = "Enter: next step · n: skip case · b: previous case · r: replay case\nSelect: 1 Markdown · 2 Go · 3 Python · 4 JS · 5 TS · 6 shell · 7 burst · 8 Python script\nCtrl-B 1 focuses controls; Ctrl-D quits.\n"

func replayPreviewBoundaries(ctx context.Context, auto *autoLiveDiff, workspace string, out io.Writer, advance <-chan byte, initial byte) error {
	turn := codexTurnMetadata{RequestKind: "turn", TurnID: "boundary-preview"}
	auto.observe(workspace, "root", turn)
	auto.beginTurn(workspace, "root", turn)
	auto.requestLaunch(workspace, "root")
	// A dedicated target keeps the script replay independent of edits/renames
	// performed by the original multi-pane story in this temporary workspace.
	if err := os.WriteFile(filepath.Join(workspace, "boundary-python-target.go"), []byte(previewFiles["internal/pane/launch.go"]), 0o600); err != nil {
		return err
	}
	show := func(text string) error {
		_, err := fmt.Fprint(out, "\x1b[2J\x1b[H"+strings.ReplaceAll(text, "\n", "\r\n"))
		return err
	}
	wait := func() (byte, error) {
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case key, ok := <-advance:
			if !ok {
				return 0, io.EOF
			}
			return key, nil
		}
	}
	baseline := startLiveDiffPreview(ctx, auto.events, workspace, "root", applyPatchToolName)
	defer baseline.stop()
	baseline.finish("*** Begin Patch\n*** Add File: previous.txt\n+PREVIOUS DIFF: keep me until new source is ready.\n*** End Patch\n")
	select {
	case <-baseline.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	cases := previewBoundaryCases()
	index, phase := 0, -1
	var worker *liveDiffPreviewWorker
	defer func() {
		if worker != nil {
			worker.stop()
		}
	}()
	if err := show("DIFF STREAMING CHECK\n\nCompare EXPECT with the diff pane. These are provisional previews; no fixture edits are executed.\n\n" + previewBoundaryControls + "\nReady: press Enter to start.\n"); err != nil {
		return err
	}
	key := initial
	for {
		if key == 0 {
			var err error
			key, err = wait()
			if err != nil {
				return err
			}
		}
		restart := phase < 0
		switch {
		case key >= '1' && int(key-'1') < len(cases):
			index, phase, restart = int(key-'1'), 0, true
		case key == 'r':
			phase, restart = 0, true
		case key == 'b':
			index, phase, restart = (index+len(cases)-1)%len(cases), 0, true
		case key == 'n' || key == 's':
			index, phase, restart = (index+1)%len(cases), 0, true
		default:
			phase++
			if phase >= len(cases[index].Steps) {
				worker.finish(cases[index].Final)
				select {
				case <-worker.done:
				case <-ctx.Done():
					return ctx.Err()
				}
				index, phase, restart = (index+1)%len(cases), 0, true
			}
		}
		key = 0
		fixture := cases[index]
		if restart {
			if worker != nil {
				worker.stop()
			}
			worker = startLiveDiffPreview(ctx, auto.events, workspace, "root", fixture.Kind)
			worker.mu.Lock()
			worker.preview.Caller = "/root"
			worker.mu.Unlock()
		}
		checkpoint := fixture.Steps[phase]
		expect := "KEEP the preceding diff. No new file body yet."
		if len(checkpoint.Want) != 0 {
			expect = "VISIBLE: " + strings.Join(checkpoint.Want, " | ")
		}
		if len(checkpoint.Absent) != 0 {
			expect += "\nHIDDEN:  " + strings.Join(checkpoint.Absent, " | ")
		}
		worker.appendDelta(checkpoint.Delta)
		heading := fmt.Sprintf("CASE %d/%d · %s\nSTEP %d/%d · %s\n\nEXPECT IN DIFF PANE\n%s\n\nTransport: %s\nFile: %s\n\nCHECKPOINT %d.%d READY\nInput sent; watch the live diff settle. Controls remain active.\n\n", index+1, len(cases), fixture.Name, phase+1, len(fixture.Steps), checkpoint.Label, expect, fixture.Kind, fixture.Path, index+1, phase+1)
		if err := show(heading + previewBoundaryControls); err != nil {
			return err
		}
	}
}

func TestTerminalUIPreviewBoundaryExpectations(t *testing.T) {
	workspace := t.TempDir()
	writeTestFile(t, filepath.Join(workspace, "boundary-python-target.go"), previewFiles["internal/pane/launch.go"])
	broker := newLiveDiffBroker(t.Context())
	broker.setScope(liveDiffScope{Workspaces: map[string]map[string]bool{workspace: {"root": true}}})
	sub := broker.subscribe()
	<-sub.events
	var pane liveDiffPreviewPane
	baseline := liveDiffPreviewWorker{ctx: t.Context(), kind: applyPatchToolName}
	previous, _ := baseline.project("*** Begin Patch\n*** Add File: previous.txt\n+PREVIOUS DIFF\n*** End Patch\n", workspace, true)
	previous.ID, previous.Workspace, previous.Thread, previous.Complete = "baseline", workspace, "root", true
	pane.update(previous)
	for index, fixture := range previewBoundaryCases() {
		worker := liveDiffPreviewWorker{ctx: t.Context(), kind: fixture.Kind}
		var input string
		for phase, checkpoint := range fixture.Steps {
			input += checkpoint.Delta
			preview, recognized := worker.project(input, workspace, false)
			if !recognized && phase != 0 {
				t.Fatalf("%s step %d: input was not an edit", fixture.Name, phase+1)
			}
			preview.ID, preview.Workspace, preview.Thread = fmt.Sprint(index), workspace, "root"
			if !recognized {
				preview.Status = liveDiffPreviewEdit
			}
			broker.publishPreview(preview, false)
			for _, event := range broker.takePreviews(sub) {
				if event.Preview != nil {
					pane.update(*event.Preview)
				}
			}
			lines, err := pane.render(t.Context(), workspace, livediff.DarkTheme, 110, 24)
			if err != nil {
				t.Fatal(err)
			}
			frame := ansi.Strip(strings.Join(lines, "\n"))
			for _, want := range checkpoint.Want {
				if !strings.Contains(frame, want) {
					t.Fatalf("%s step %d: missing %q in %q", fixture.Name, phase+1, want, frame)
				}
			}
			for _, absent := range checkpoint.Absent {
				if strings.Contains(frame, absent) {
					t.Fatalf("%s step %d: exposed %q in %q", fixture.Name, phase+1, absent, frame)
				}
			}
			if phase == 0 && (len(pane.order) != 1 || pane.order[0] != previous.ID) {
				t.Fatalf("%s header replaced preceding diff", fixture.Name)
			}
		}
		if !strings.HasPrefix(fixture.Final, input) {
			t.Fatalf("%s deltas are not a prefix of final input", fixture.Name)
		}
		previous, _ = worker.project(fixture.Final, workspace, true)
		previous.ID, previous.Workspace, previous.Thread, previous.Complete = fmt.Sprint(index), workspace, "root", true
		pane.update(previous)
	}
}

func TestTerminalUIPreviewPythonLiteralStreamsThroughPTY(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	path := filepath.Join(workspace, "internal/pane/launch.go")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	original := previewFiles["internal/pane/launch.go"]
	writeTestFile(t, path, original)
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	connection, broker, _ := liveDiffTestBroker(t, store, liveDiffScope{Workspaces: map[string]map[string]bool{workspace: {"thread": true}}})
	ui := startLiveDiffTerminal(t, workspace, store.directory, connection, 22)
	ui.frame(t, func(frame string) bool { return strings.Contains(frame, "STREAM · v diff") })
	_, _, command := previewPythonEditCommand()
	input := string(mustTestJSON(t, map[string]string{"cmd": command, "workdir": workspace}))
	// Both checkpoints precede the replacement literal's closing quote,
	// its assignment completion, and the final p.write_text call.
	beforeMarker := func(marker string) int {
		at := strings.LastIndex(input, marker)
		if at < 0 {
			t.Fatalf("missing fixture marker %q", marker)
		}
		end := strings.LastIndex(input[:at], "\\n")
		if end < 0 {
			t.Fatalf("missing newline before %q", marker)
		}
		return end + 2
	}
	firstEnd, secondEnd := beforeMarker("if requested"), beforeMarker("return frame")
	worker := startLiveDiffPreview(t.Context(), broker, workspace, "thread", nativeExecCommandToolName)
	t.Cleanup(worker.stop)
	worker.appendDelta(input[:firstEnd])
	first := waitExecScopePreview(t, broker, func(preview liveDiffPreview) bool {
		return len(preview.Files) == 1 && strings.Contains(preview.Files[0].Diff, "+\tframe := <-frames")
	})
	if first.Complete || strings.Contains(first.Files[0].Diff, "+\tif requested") {
		t.Fatalf("first target statement was not isolated: %+v", first)
	}
	ui.frame(t, func(frame string) bool { return strings.Contains(ansi.Strip(frame), "frame := <-frames") })
	worker.appendDelta(input[firstEnd:secondEnd])
	second := waitExecScopePreview(t, broker, func(preview liveDiffPreview) bool {
		return len(preview.Files) == 1 && strings.Contains(preview.Files[0].Diff, "return \"open: \" + frame")
	})
	if second.Complete || strings.Contains(second.Files[0].Diff, "+\treturn frame") {
		t.Fatalf("second target statement waited for full input or exposed its tail: %+v", second)
	}
	ui.frame(t, func(frame string) bool {
		plain := ansi.Strip(frame)
		if !strings.Contains(plain, "return \"open: \" + frame") {
			return false
		}
		removed, added := strings.Index(plain, "return \"open\""), strings.Index(plain, "frame := <-frames")
		if removed < 0 || added < 0 || removed > added {
			t.Fatalf("Python preview reordered removed rows after arriving additions:\n%s", plain)
		}
		return true
	})
	worker.finish(input)
	ui.frame(t, func(frame string) bool {
		return strings.Contains(ansi.Strip(frame), "✓ M") && strings.Contains(ansi.Strip(frame), "return frame")
	})
	if got, err := os.ReadFile(path); err != nil || string(got) != original {
		t.Fatalf("preview executed the Python edit: %q %v", got, err)
	}
	ui.quit(t)
}

func TestTerminalUIPreviewStatementBurstPTY(t *testing.T) {
	for _, source := range liveDiffStatementBurstCases() {
		for _, kind := range []string{applyPatchToolName, nativeExecCommandToolName, "exec"} {
			t.Run(source.first+"/"+kind, func(t *testing.T) {
				t.Parallel()
				workspace := t.TempDir()
				store, err := openMekugiReplayStore(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				connection, broker, _ := liveDiffTestBroker(t, store, liveDiffScope{Workspaces: map[string]map[string]bool{workspace: {"thread": true}}})
				ui := startLiveDiffTerminal(t, workspace, store.directory, connection, 22)
				ui.frame(t, func(frame string) bool { return strings.Contains(frame, "STREAM · v diff") })
				input := "*** Begin Patch\n*** Add File: " + source.path + "\n+" + strings.ReplaceAll(strings.TrimSuffix(source.body, "\n"), "\n", "\n+") + "\n*** End Patch\n"
				if kind == "exec" {
					input = "await tools.apply_patch(" + strconv.Quote(input) + ");"
				} else if kind == nativeExecCommandToolName {
					command := "python3 - <<'PY'\nfrom pathlib import Path\nPath(" + strconv.Quote(source.path) + ").write_text(" + strconv.Quote(source.body) + ")\nPY\n"
					input = string(mustTestJSON(t, map[string]string{"cmd": command, "workdir": workspace}))
				}
				worker := startLiveDiffPreview(t.Context(), broker, workspace, "thread", kind)
				t.Cleanup(worker.stop)
				// Even immediate call completion must not bypass queued target
				// statement boundaries and fade in the entire completed diff.
				worker.appendDelta(input)
				worker.finish(input)
				ui.frame(t, func(frame string) bool {
					plain := ansi.Strip(frame)
					if !strings.Contains(plain, source.first) {
						return false
					}
					if strings.Contains(plain, source.second) {
						t.Fatalf("two target statements first appeared in the same rendered frame:\n%s", plain)
					}
					return true
				})
				ui.frame(t, func(frame string) bool { return strings.Contains(ansi.Strip(frame), source.second) })
				ui.quit(t)
			})
		}
	}
}
