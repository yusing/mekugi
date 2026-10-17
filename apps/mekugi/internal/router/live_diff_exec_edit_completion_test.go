package router

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	previewview "github.com/yusing/mekugi/modules/livepreview/terminalview"

	"github.com/yusing/mekugi/apps/mekugi/internal/executionobservation"

	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"

	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/libraries/commandanalysis"
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
	proxy := &observationFixture{review: testReview(auto), execWindows: &executionobservation.Windows{}}
	startObservedExecTest(t, proxy, workspace, "main", "turn", "cell", source)
	// The inner move completes while the enclosing cell remains open.
	if err := os.Rename(filepath.Join(workspace, "command-timing.txt.new"), filepath.Join(workspace, "command-timing.txt")); err != nil {
		t.Fatal(err)
	}
	u := nativeBatchFixture(t)
	for _, event := range testReview(auto).TakePreviews(sub) {
		if event.Preview != nil {
			u.preview(*event.Preview, false)
		}
	}
	revealNativeDock(terminalTest(u))
	if len(terminalTest(u).Live.Order) != 0 {
		t.Fatal("move replaced the transcript or closed the host-owned cell")
	}
	screen := vt.NewEmulator(100, 28)
	defer screen.Close()
	if err := u.testPaint(screen, 100, 28); err != nil {
		t.Fatal(err)
	}
	assertNativeUISnapshot(t, "native-dock-before-reveal", strings.Split(screen.String(), "\n"))
}

func TestNativeUIExecEditCompletionRetainsWorkingStatusAndClearsDock(t *testing.T) {
	t.Parallel()
	u, _ := newAppServerTestUI()

	defer terminalTest(u).Diff.Close()
	u.attached.presentation.Status.Text = "Working"
	workspace := t.TempDir()
	preview := previewmodel.Preview{ID: "running:edit-then-test", Workspace: workspace, Thread: "thread", Caller: "/root", Tool: nativeExecCommandToolName, Status: previewmodel.PreviewRunning, Input: "observed edit"}
	u.preview(preview, false)
	revealNativeDock(terminalTest(u))
	preview.Complete = true
	u.preview(preview, false)
	revealNativeDock(terminalTest(u))
	if terminalTest(u).Live.Live() != 0 || u.attached.presentation.Status.Text != "Working" {
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
	terminalTest(u).Live.Tick(time.Now().Add(previewview.CompletionHold + time.Millisecond))
	if len(terminalTest(u).Live.Order) != 0 || u.attached.presentation.Status.Text != "Working" {
		t.Fatal("completed dock did not clear independently of process Working status")
	}
	// A final host observation must not create a second, late completion card.
	u.preview(previewmodel.Preview{ID: "late:edit-then-test", Workspace: workspace, Tool: nativeExecCommandToolName, Complete: true, Status: previewmodel.PreviewRunning}, false)
	revealNativeDock(terminalTest(u))
	if len(terminalTest(u).Live.Order) != 0 {
		t.Fatal("late shell completion resurrected the dock")
	}
}
