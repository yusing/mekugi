package router

import (
	"bufio"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/internal/ui/diffview"
)

func TestExecEditCompletionFastPythonWriteWhileTestRuns(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("Python is required for this shell execution fixture")
	}
	workspace := t.TempDir()
	writeTestFile(t, filepath.Join(workspace, "source.go"), "before\n")
	// The second process is an actual read-only test. It signals that the
	// edit landed, then waits for us to inspect the UI before it can exit.
	command := "python3 - <<'PY'\np='source.go'\ns=open(p).read().replace('before', 'after')\nopen(p,'w').write(s)\nPY\n" +
		`python3 -c 'import sys; assert open("source.go").read() == "after\n"; print("test running", flush=True); assert sys.stdin.readline().strip() == "continue"'`
	observation, ok := captureExecObservation([]execCommandInput{{Command: command, Workdir: workspace, Shell: "bash"}}, false, false, execCaptureEnv{directory: workspace})
	if !ok || observation == nil {
		t.Fatal("missing edit observation")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "env", "-u", "BASH_ENV", "bash", "-c", command)
	cmd.Dir = workspace
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cmd.ProcessState == nil {
			cancel()
			_ = cmd.Wait()
		}
	}()
	if line, err := bufio.NewReader(output).ReadString('\n'); err != nil || line != "test running\n" {
		t.Fatalf("test did not start after Python edit: %q, %v", line, err)
	}
	broker := newLiveDiffBroker(ctx)
	broker.setScope(liveDiffScope{Workspaces: map[string]map[string]bool{workspace: {"thread": true}}})
	sub := broker.subscribe()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		runExecScopePreview(ctx, broker, *observation, diffview.Preview{ID: "fast-edit", Workspace: workspace, Thread: "thread", Caller: "/root"}, nil)
	}()
	select {
	case <-finished:
	case <-ctx.Done():
		t.Fatal("edit display waited for the blocked test to finish")
	}
	u, _ := newAppServerTestUI()
	u.ensureShell()
	defer u.shell.diffScreen.Close()
	completed := false
	for _, event := range broker.takePreviews(sub) {
		if event.Preview != nil {
			completed = completed || event.Preview.Complete
			u.shell.preview(*event.Preview)
		}
	}
	if !completed || len(u.shell.liveDock.Order) != 0 {
		t.Fatal("fast observed edit flashed a stale card while the test was running")
	}
	if _, err := io.WriteString(input, "continue\n"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("read-only test failed: %v", err)
	}
}

func execEditThenTestObservation(t *testing.T, workspace string) (execObservation, string) {
	t.Helper()
	path := filepath.Join(workspace, "source.go")
	writeTestFile(t, path, "before\n")
	command := "python3 - <<'PY'\np='source.go'\ns=open(p).read().replace('before', 'after')\nopen(p,'w').write(s)\nPY\nenv -u BASH_ENV go test ./..."
	observation, ok := captureExecObservation([]execCommandInput{{Command: command, Workdir: workspace, Shell: "bash"}}, false, true, execCaptureEnv{directory: workspace})
	if !ok || observation == nil || len(observation.Files) != 1 {
		t.Fatalf("edit and test target capture = %+v, observed=%v", observation, ok)
	}
	return *observation, path
}

func TestExecEditCompletionUsesCapturedBaseline(t *testing.T) {
	workspace := t.TempDir()
	observation, path := execEditThenTestObservation(t, workspace)
	if err := os.WriteFile(path, []byte("after\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	expected := execPreviewExpected(t.Context(), observation)
	if len(expected) != 1 || expected[path].Diff == "" || !strings.Contains(expected[path].Diff, "-before") || !strings.Contains(expected[path].Diff, "+after") {
		t.Fatalf("expected edit must compare against pre-call capture, not current disk: %+v", expected)
	}
	for name, mutate := range map[string]func(*execObservation){
		"uncaptured": func(o *execObservation) { o.Files = nil },
		"binary":     func(o *execObservation) { o.Files[0].Kind = execFileBinary },
		"incomplete": func(o *execObservation) { o.Files[0].Error = "capture incomplete" },
	} {
		t.Run(name, func(t *testing.T) {
			copy := observation
			copy.Files = append([]execFileSnapshot(nil), observation.Files...)
			mutate(&copy)
			if got := execPreviewExpected(t.Context(), copy); len(got) != 0 {
				t.Fatalf("unsafe source yielded predicted edit: %+v", got)
			}
		})
	}
	testOnly := execObservation{Commands: []execCommandInput{{Command: "env -u BASH_ENV go test ./...", Workdir: workspace, Shell: "bash"}}, Class: execOpaque.String()}
	if got := execPreviewExpected(t.Context(), testOnly); len(got) != 0 {
		t.Fatalf("test-only command yielded predicted edit: %+v", got)
	}
}

func TestExecEditCompletionBeforeFollowingTestFinishes(t *testing.T) {
	workspace := t.TempDir()
	observation, path := execEditThenTestObservation(t, workspace)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	broker := newLiveDiffBroker(ctx)
	broker.setScope(liveDiffScope{Workspaces: map[string]map[string]bool{workspace: {"thread": true}}})
	sub := broker.subscribe()
	registry := &execWindowRegistry{}
	registry.open(&execWindow{ref: "edit-then-test", roots: []string{workspace}, thread: "thread"})
	registry.preview("edit-then-test", observation, broker, workspace, "thread", "/root")
	if err := os.WriteFile(path, []byte("aft"), 0o600); err != nil {
		t.Fatal(err)
	}
	partial := waitExecScopePreview(t, broker, func(preview diffview.Preview) bool {
		return preview.ID == "running:edit-then-test" && len(preview.Files) == 1
	})
	if partial.Complete {
		t.Fatal("partial Python write completed the edit card")
	}
	// Consume the partial frame, as the native viewer does while the process runs.
	broker.takePreviews(sub)
	if err := os.WriteFile(path, []byte("after\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var completed diffview.Preview
	deadline := time.After(5 * time.Second)
	for completed.ID == "" {
		select {
		case <-sub.previewReady:
			for _, event := range broker.takePreviews(sub) {
				if event.Preview != nil && event.Preview.ID == "running:edit-then-test" && event.Preview.Complete {
					completed = *event.Preview
				}
			}
		case <-deadline:
			t.Fatal("completion was replaced by a removal marker before the native viewer could render it")
		}
	}
	if completed.Status != diffview.PreviewRunning || completed.Footer != "observed edit" || len(completed.Files) != 1 {
		t.Fatalf("edit completion lost the observed content: %+v", completed)
	}
	registry.mu.Lock()
	window := registry.find("edit-then-test")
	open := window != nil && window.closed.IsZero()
	registry.mu.Unlock()
	if !open {
		t.Fatal("display completion closed the host observation window before test outcome")
	}
	// There is no completed change event until the host command finishes.
	for _, event := range broker.takePreviews(sub) {
		if event.Kind == "change" {
			t.Fatalf("edit preview persisted change evidence before host result: %+v", event)
		}
	}
}

func TestNativeUIExecEditCompletionRetainsWorkingStatusAndClearsDock(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.ensureShell()
	defer u.shell.diffScreen.Close()
	u.status = "Working"
	workspace := t.TempDir()
	preview := diffview.Preview{ID: "running:edit-then-test", Workspace: workspace, Thread: "thread", Caller: "/root", Tool: nativeExecCommandToolName, Status: diffview.PreviewRunning, Input: "observed edit"}
	u.shell.preview(preview)
	preview.Complete = true
	u.shell.preview(preview)
	if u.shell.liveDock.Live() != 0 || u.status != "Working" {
		t.Fatal("edit completion changed process status or retained an active dock")
	}
	screen := vt.NewEmulator(120, 40)
	defer screen.Close()
	if err := u.paint(screen, 120, 40); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(screen.String(), "LIVE ·") {
		t.Fatal("completed edit was not rendered during its minimum display time")
	}
	u.shell.dockShown = time.Now().Add(-nativeDockMinimum - time.Millisecond)
	u.shell.animating(time.Now())
	if len(u.shell.liveDock.Order) != 0 || u.status != "Working" {
		t.Fatal("completed dock did not clear independently of process Working status")
	}
	// A final host observation must not create a second, late completion card.
	u.shell.preview(diffview.Preview{ID: "late:edit-then-test", Workspace: workspace, Tool: nativeExecCommandToolName, Complete: true, Status: diffview.PreviewRunning})
	if len(u.shell.liveDock.Order) != 0 {
		t.Fatal("late shell completion resurrected the dock")
	}
}

func TestExecEditCompletionTerminalFrameBeforeHostExit(t *testing.T) {
	workspace := t.TempDir()
	observation, path := execEditThenTestObservation(t, workspace)
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	connection, broker, _ := liveDiffTestBroker(t, store, liveDiffScope{Workspaces: map[string]map[string]bool{workspace: {"thread": true}}})
	terminal := startLiveDiffTerminal(t, workspace, store.directory, connection, 22)
	terminal.frame(t, func(frame string) bool { return strings.Contains(frame, "STREAM · v diff") })
	registry := &execWindowRegistry{}
	registry.open(&execWindow{ref: "edit-and-test", roots: []string{workspace}, thread: "thread"})
	defer registry.close("edit-and-test")
	registry.preview("edit-and-test", observation, broker, workspace, "thread", "/root")
	writeTestFile(t, path, "aft")
	terminal.frame(t, func(frame string) bool {
		frame = ansi.Strip(frame)
		return strings.Contains(frame, "observed so far") && strings.Contains(frame, "+aft")
	})
	writeTestFile(t, path, "after\n")
	frame := terminal.frame(t, func(frame string) bool {
		frame = ansi.Strip(frame)
		return strings.Contains(frame, "observed edit") && strings.Contains(frame, "+after") && !strings.Contains(frame, "observed so far")
	})
	if strings.Contains(frame, "other writes unknown") || strings.Contains(frame, "go test") {
		t.Fatalf("test observation leaked into the completed edit card: %s", frame)
	}
	registry.mu.Lock()
	open := registry.find("edit-and-test").closed.IsZero()
	registry.mu.Unlock()
	if !open {
		t.Fatal("rendered completion finalized the still-running host invocation")
	}
	terminal.quit(t)
}
