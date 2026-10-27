package terminalsession

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

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
