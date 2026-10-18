package launch

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"

	"github.com/yusing/mekugi/libraries/diffrender"

	"github.com/charmbracelet/x/ansi"
	previewview "github.com/yusing/mekugi/modules/livepreview/terminalview"
)

func TestLiveDiffPreviewBoundaryExpectations(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	writeTestFile(t, filepath.Join(workspace, "boundary-python-target.go"), previewFiles["internal/pane/launch.go"])
	broker := newLivePreview(t.Context())
	setLiveScope(broker, previewmodel.Scope{Workspaces: map[string]map[string]bool{workspace: {"root": true}}})
	sub := testReview(broker).Subscribe()
	<-sub.Events()
	var pane previewview.PreviewPane
	baselineContext := t.Context()
	baselineProject := previewProjector(baselineContext, applyPatchToolName)
	previous, _ := baselineProject("*** Begin Patch\n*** Add File: previous.txt\n+PREVIOUS DIFF\n*** End Patch\n", workspace, true)
	previous.ID, previous.Workspace, previous.Thread, previous.Complete = "baseline", workspace, "root", true
	pane.Update(previous, time.Now())
	for index, fixture := range previewBoundaryCases() {
		previewContext := t.Context()
		previewProject := previewProjector(previewContext, fixture.Kind)
		var input string
		for phase, checkpoint := range fixture.Steps {
			input += checkpoint.Delta
			preview, recognized := previewProject(input, workspace, false)
			if !recognized && phase != 0 {
				t.Fatalf("%s step %d: input was not an edit", fixture.Name, phase+1)
			}
			preview.ID, preview.Workspace, preview.Thread = fmt.Sprint(index), workspace, "root"
			if !recognized {
				preview.Status = previewmodel.PreviewEdit
			}
			broker.Update(preview, false)
			for _, event := range testReview(broker).TakePreviews(sub) {
				if event.Preview != nil {
					pane.Update(*event.Preview, time.Now())
				}
			}
			lines, err := pane.RenderBatch(t.Context(), "", workspace, diffrender.DarkTheme, 110, 24, 15)
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
			if phase == 0 && pane.Order[len(pane.Order)-1] != previous.ID {
				t.Fatalf("%s header replaced preceding diff", fixture.Name)
			}
		}
		if !strings.HasPrefix(fixture.Final, input) {
			t.Fatalf("%s deltas are not a prefix of final input", fixture.Name)
		}
		previous, _ = previewProject(fixture.Final, workspace, true)
		previous.ID, previous.Workspace, previous.Thread, previous.Complete = fmt.Sprint(index), workspace, "root", true
		pane.Update(previous, time.Now())
	}
}
