package router

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
)

func TestLiveDiffPreviewBoundaryExpectations(t *testing.T) {
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
