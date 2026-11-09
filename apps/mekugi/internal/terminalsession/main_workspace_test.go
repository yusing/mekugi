package terminalsession

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func TestUISnapshotMainWorkspacePaths(t *testing.T) {
	const workspace = "/home/dev/projects/workspace/.worktrees/distinctive-project"
	for _, width := range []int{40, 120} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			view := sessionpresentation.View{DisplayedWorkspace: workspace, Caption: sessionpresentation.Caption{Model: "model"}}
			var main Main
			frame := main.layout(mainInput{Session: view}, new(activityui.Painter), width, 3, 0)
			line := ansi.Strip(frame.Rows[len(frame.Rows)-1])
			if !strings.Contains(line, "e-project • model") || strings.Contains(line, "/home/dev") {
				t.Fatalf("workspace suffix displaced by prefix: %q", line)
			}
			if view.DisplayedWorkspace != workspace {
				t.Fatal("painting changed the operational workspace")
			}
			uisnapshot.Assert(t, fmt.Sprintf("testdata/snapshots/workspace_path_%d.txt", width), strings.Join(frame.Rows, "\n"))
			t.Logf("Rendered %d-column composer:\n%s", width, strings.Join(frame.Rows, "\n"))
		})
	}
}

func TestMainComposerWorkspace(t *testing.T) {
	for _, workspace := range []string{"/main", "/child", "/work/界/long-project"} {
		for _, width := range []int{12, 20, 36, 80} {
			view := sessionpresentation.View{Info: sessionpresentation.MainInfo{Workspace: "/main"}, DisplayedWorkspace: workspace, Caption: sessionpresentation.Caption{Model: "model"}}
			var main Main
			frame := main.layout(mainInput{Session: view}, new(activityui.Painter), width, 3, 0)
			line := ansi.Strip(frame.Rows[len(frame.Rows)-1])
			if ansi.StringWidth(line) != width || !strings.HasPrefix(line, "╰──") {
				t.Fatalf("workspace %q, width %d: %q", workspace, width, line)
			}
			if width == 80 && !strings.HasSuffix(line, workspace+" • model • 0% ─╯") {
				t.Fatalf("workspace and model alignment: %q", line)
			}
			if width >= 20 && (!strings.Contains(line, " • m") || !strings.HasSuffix(line, " • 0% ─╯")) {
				t.Fatalf("workspace displaced model or usage: %q", line)
			}
		}
	}
}

func TestMainComposerTierWithoutModel(t *testing.T) {
	view := sessionpresentation.View{DisplayedWorkspace: "/workspace", Caption: sessionpresentation.Caption{Tier: "flex"}}
	var main Main
	frame := main.layout(mainInput{Session: view}, new(activityui.Painter), 80, 3, 0)
	line := ansi.Strip(frame.Rows[len(frame.Rows)-1])
	if !strings.HasSuffix(line, "/workspace • flex • 0% ─╯") {
		t.Fatalf("tier without model: %q", line)
	}
}
