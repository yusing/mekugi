package router

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"

	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/libraries/commandanalysis"
	"github.com/yusing/mekugi/libraries/commandanalysis/execscope"
	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
)

func TestUISnapshotNativeMoveKeepsTranscript(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	writeTestFile(t, filepath.Join(workspace, "command-timing.txt.new"), "Review @src\n\nAttached src (3 lines)\n")
	script := "mv command-timing.txt.new command-timing.txt; git status --short"
	source := fmt.Sprintf("await tools.exec_command({cmd:%q,workdir:%q}); await tools.exec_command({cmd:'read -r answer',workdir:%q});", script, workspace, workspace)
	commands, dynamic := commandanalysis.LiteralExecCommands(source, workspace, "bash")
	observation, ok := changecapture.Capture(t.Context(), commands, dynamic, true, changecapture.Options{Directory: workspace})
	if !ok || observation == nil || len(observation.Files) == 0 {
		t.Fatal("move lost its retained capture")
	}
	auto := newLivePreview(t.Context())
	defer auto.Close()
	auto.Enable()
	setLiveScope(auto, previewmodel.Scope{Workspaces: map[string]map[string]bool{workspace: {"main": true}}})
	sub := testReview(auto).Subscribe()
	proxy := &mekugiProxy{review: testReview(auto), execWindows: &execWindowRegistry{}}
	transform := &mekugiResponseTransform{control: proxy.control, review: proxy.review, execWindows: proxy.execWindows, calls: proxy.calls, retained: proxy.replayStore, previewTiming: proxy.previewTiming, directory: workspace, threadID: "main", shellThreadID: "main", shellTurnID: "turn"}
	transform.openExecWindow("cell", observation, nil)
	defer proxy.execWindows.close("cell")
	if proxy.execWindows.find("cell").previewCancel != nil {
		t.Fatal("move opened a polling preview while the enclosing cell could keep waiting")
	}
	// The inner move completes while the enclosing cell remains open.
	if err := os.Rename(filepath.Join(workspace, "command-timing.txt.new"), filepath.Join(workspace, "command-timing.txt")); err != nil {
		t.Fatal(err)
	}
	u := nativeBatchFixture(t)
	for _, event := range testReview(auto).TakePreviews(sub) {
		if event.Preview != nil {
			u.shell.preview(*event.Preview, false)
		}
	}
	revealNativeDock(u.shell)
	if len(u.shell.liveDock.Order) != 0 || !proxy.execWindows.find("cell").closed.IsZero() {
		t.Fatal("move replaced the transcript or closed the host-owned cell")
	}
	screen := vt.NewEmulator(100, 28)
	defer screen.Close()
	if err := u.testPaint(screen, 100, 28); err != nil {
		t.Fatal(err)
	}
	assertNativeUISnapshot(t, "native-dock-before-reveal", strings.Split(screen.String(), "\n"))
}

func TestExecEditCompletionFastPythonWriteWhileTestRuns(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("Python is required for this shell execution fixture")
	}
	workspace := t.TempDir()
	writeTestFile(t, filepath.Join(workspace, "source.go"), strings.Repeat("context\n", 20)+"before\n"+strings.Repeat("context\n", 20))
	// The second process is an actual read-only test. It signals that the
	// edit landed, then waits for us to inspect the UI before it can exit.
	command := "python3 - <<'PY'\np='source.go'\ns=open(p).read().replace('before', 'after')\nopen(p,'w').write(s)\nPY\n" +
		`python3 -c 'import sys; assert open("source.go").read() == "context\n"*20 + "after\n" + "context\n"*20; print("test running", flush=True); assert sys.stdin.readline().strip() == "continue"'`
	observation, ok := changecapture.Capture(t.Context(), []commandanalysis.Command{{Command: command, Workdir: workspace, Shell: "bash"}}, false, false, changecapture.Options{Directory: workspace})
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
	broker := newLivePreview(ctx)
	setLiveScope(broker, previewmodel.Scope{Workspaces: map[string]map[string]bool{workspace: {"thread": true}}})
	sub := testReview(broker).Subscribe()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		runExecScopePreview(ctx, broker, *observation, previewmodel.Preview{ID: "fast-edit", Workspace: workspace, Thread: "thread", Caller: "/root"}, nil, nil)
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
	for _, event := range testReview(broker).TakePreviews(sub) {
		if event.Preview != nil {
			completed = completed || event.Preview.Complete
			u.shell.preview(*event.Preview, false)
			revealNativeDock(u.shell)
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

func execEditThenTestObservation(t *testing.T, workspace string) (changecapture.Observation, string) {
	t.Helper()
	path := filepath.Join(workspace, "source.go")
	writeTestFile(t, path, "before\n")
	command := "python3 - <<'PY'\np='source.go'\ns=open(p).read().replace('before', 'after')\nopen(p,'w').write(s)\nPY\nenv -u BASH_ENV go test ./..."
	observation, ok := changecapture.Capture(t.Context(), []commandanalysis.Command{{Command: command, Workdir: workspace, Shell: "bash"}}, false, true, changecapture.Options{Directory: workspace})
	if !ok || observation == nil || len(observation.Files) != 1 {
		t.Fatalf("edit and test target capture = %+v, observed=%v", observation, ok)
	}
	return *observation, path
}

func TestExecEditCompletionUsesCapturedBaseline(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	observation, path := execEditThenTestObservation(t, workspace)
	if err := os.WriteFile(path, []byte("after\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	expected := previewmodel.Expected(t.Context(), observation)
	if len(expected) != 1 || expected[path].Diff == "" || !strings.Contains(expected[path].Diff, "-before") || !strings.Contains(expected[path].Diff, "+after") {
		t.Fatalf("expected edit must compare against pre-call capture, not current disk: %+v", expected)
	}
	for name, mutate := range map[string]func(*changecapture.Observation){
		"uncaptured": func(o *changecapture.Observation) { o.Files = nil },
		"binary":     func(o *changecapture.Observation) { o.Files[0].Kind = changecapture.Binary },
		"incomplete": func(o *changecapture.Observation) { o.Files[0].Error = "capture incomplete" },
	} {
		t.Run(name, func(t *testing.T) {
			copy := observation
			copy.Files = append([]changecapture.File(nil), observation.Files...)
			mutate(&copy)
			if got := previewmodel.Expected(t.Context(), copy); len(got) != 0 {
				t.Fatalf("unsafe source yielded predicted edit: %+v", got)
			}
		})
	}
	testOnly := changecapture.Observation{Commands: []commandanalysis.Command{{Command: "env -u BASH_ENV go test ./...", Workdir: workspace, Shell: "bash"}}, Class: execscope.Opaque.String()}
	if got := previewmodel.Expected(t.Context(), testOnly); len(got) != 0 {
		t.Fatalf("test-only command yielded predicted edit: %+v", got)
	}
}

func TestExecEditCompletionBeforeFollowingTestFinishes(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	observation, path := execEditThenTestObservation(t, workspace)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	broker := newLivePreview(ctx)
	setLiveScope(broker, previewmodel.Scope{Workspaces: map[string]map[string]bool{workspace: {"thread": true}}})
	sub := testReview(broker).Subscribe()
	registry := &execWindowRegistry{}
	registry.open(&execWindow{ref: "edit-then-test", roots: []string{workspace}, thread: "thread"})
	registry.preview("edit-then-test", observation, broker, workspace, "thread", "/root", nil)
	if err := os.WriteFile(path, []byte("aft"), 0o600); err != nil {
		t.Fatal(err)
	}
	partial := waitExecScopePreview(t, broker, func(preview previewmodel.Preview) bool {
		return preview.ID == "running:edit-then-test" && len(preview.Files) == 1
	})
	if partial.Complete || partial.Tool != "python3" {
		t.Fatal("partial Python write completed the edit card")
	}
	// Consume the partial frame, as the native viewer does while the process runs.
	testReview(broker).TakePreviews(sub)
	if err := os.WriteFile(path, []byte("after\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var completed previewmodel.Preview
	deadline := time.After(5 * time.Second)
	for completed.ID == "" {
		select {
		case <-sub.Ready():
			for _, event := range testReview(broker).TakePreviews(sub) {
				if event.Preview != nil && event.Preview.ID == "running:edit-then-test" && event.Preview.Complete {
					completed = *event.Preview
				}
			}
		case <-deadline:
			t.Fatal("completion was replaced by a removal marker before the native viewer could render it")
		}
	}
	if completed.Tool != "python3" || completed.Status != previewmodel.PreviewRunning || completed.Footer != "observed edit" || len(completed.Files) != 1 {
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
	for _, event := range testReview(broker).TakePreviews(sub) {
		if event.Kind == "change" {
			t.Fatalf("edit preview persisted change evidence before host result: %+v", event)
		}
	}
}

func TestNativeUIExecEditCompletionRetainsWorkingStatusAndClearsDock(t *testing.T) {
	t.Parallel()
	u, _ := newAppServerTestUI()
	u.ensureShell()
	defer u.shell.diffScreen.Close()
	u.sessionPresentation().Status.Text = "Working"
	workspace := t.TempDir()
	preview := previewmodel.Preview{ID: "running:edit-then-test", Workspace: workspace, Thread: "thread", Caller: "/root", Tool: nativeExecCommandToolName, Status: previewmodel.PreviewRunning, Input: "observed edit"}
	u.shell.preview(preview, false)
	revealNativeDock(u.shell)
	preview.Complete = true
	u.shell.preview(preview, false)
	revealNativeDock(u.shell)
	if u.shell.liveDock.Live() != 0 || u.sessionPresentation().Status.Text != "Working" {
		t.Fatal("edit completion changed process status or retained an active dock")
	}
	screen := vt.NewEmulator(120, 40)
	defer screen.Close()
	if err := u.testPaint(screen, 120, 40); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(screen.String(), "LIVE ·") {
		t.Fatal("completed edit was not rendered during its minimum display time")
	}
	u.shell.animating(time.Now().Add(nativeDockMinimum + time.Millisecond))
	if len(u.shell.liveDock.Order) != 0 || u.sessionPresentation().Status.Text != "Working" {
		t.Fatal("completed dock did not clear independently of process Working status")
	}
	// A final host observation must not create a second, late completion card.
	u.shell.preview(previewmodel.Preview{ID: "late:edit-then-test", Workspace: workspace, Tool: nativeExecCommandToolName, Complete: true, Status: previewmodel.PreviewRunning}, false)
	revealNativeDock(u.shell)
	if len(u.shell.liveDock.Order) != 0 {
		t.Fatal("late shell completion resurrected the dock")
	}
}
