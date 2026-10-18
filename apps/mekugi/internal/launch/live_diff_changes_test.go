package launch

import (
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/yusing/mekugi/libraries/pathdisplay"

	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/libraries/commandanalysis/execscope"
	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/libraries/diffrender"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
	"github.com/yusing/mekugi/modules/changehistory/presentation"
	changeview "github.com/yusing/mekugi/modules/changehistory/terminalview"
)

func liveDiffChangesPane(t *testing.T, width, height int, captures []presentation.Chunk) *changeview.Pane {
	t.Helper()
	pane := changeview.NewPane("/w", diffrender.EnvironmentTheme(os.Getenv("COLORFGBG")))
	pane.Update((presentation.Publication{}).With(presentation.GroupCaptures(captures)), "CONNECTING")
	if err := pane.Paint(t.Context(), io.Discard, width, height); err != nil {
		t.Fatal(err)
	}
	return pane
}

func paintSavedDiff(t *testing.T, pane *changeview.Pane, width, height int) []string {
	t.Helper()
	screen := new(terminalsession.DiffScreen)
	defer screen.Close()
	rows, err := screen.Paint(t.Context(), pane, width, height)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func liveDiffLinesFile(path string, lines int) string {
	var text strings.Builder
	for i := range lines {
		fmt.Fprintf(&text, "%s_line_%02d\n", path, i)
	}
	return text.String()
}

func liveDiffCapture(key, path string, order uint64, before, after string, origin presentation.Origin) presentation.Chunk {
	return presentation.Chunk{
		Key: key, CaptureOrder: order, Origin: origin,
		Review: diffmodel.RenderReviewFile("/w/"+path, "/w/"+path, before, after),
	}
}

func TestExecObservationSourceLabel(t *testing.T) {
	for _, tc := range []struct {
		observation changecapture.Observation
		want        string
	}{
		{changecapture.Observation{}, "exec_command"},
		{changecapture.Observation{Labels: []string{"sed"}}, "sed"},
		{changecapture.Observation{Programs: []execscope.Program{{Label: "python3", Direct: true}, {Label: "make"}}}, "python3"},
		{changecapture.Observation{Labels: []string{"sed", "rm"}, Programs: []execscope.Program{{Label: "shell", Direct: true}}}, "sed+rm+…"},
	} {
		if got := tc.observation.SourceLabel(); got != tc.want {
			t.Errorf("%+v source = %q, want %q", tc.observation, got, tc.want)
		}
	}
}

func TestAgentDisplayName(t *testing.T) {
	for name, want := range map[string]string{"/root": "main", "/root/worker": "worker", "/root/a/x": "a/x", "": ""} {
		if got := pathdisplay.AgentDisplayName(name); got != want {
			t.Errorf("agentDisplayName(%q) = %q, want %q", name, got, want)
		}
	}
}
