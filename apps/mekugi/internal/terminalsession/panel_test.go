package terminalsession

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/libraries/terminalui"
)

func TestStatusPanelPathRetainsSource(t *testing.T) {
	const path = "/home/dev/projects/workspace/.worktrees/distinctive-workspace"
	view := sessionpresentation.View{Panel: &sessionpresentation.PanelView{ID: 1, Fields: []sessionpresentation.StatusField{{Group: "Session", Label: "Directory", Value: path, Path: true}}}}
	var panel StatusPanel
	panel.Update(view)
	rows := panel.Render(&terminalui.Painter{CopySource: true}, 40, 20)
	var found bool
	for _, row := range rows {
		plain, spans := terminalui.ExtractCopy(row)
		if strings.Contains(ansi.Strip(plain), "distinctive-workspace") {
			found = true
			if len(spans) != 1 || spans[0].Prefix != path {
				t.Fatalf("shortened path lost full copy source: %+v", spans)
			}
		}
	}
	if !found || view.Panel.Fields[0].Value != path {
		t.Fatal("path suffix missing or operational path changed")
	}
}

func TestStatusPanelNavigationTracksPublishedIdentity(t *testing.T) {
	v := sessionpresentation.View{Version: sessionpresentation.Version{Attachment: 1, Revision: 2}, Panel: &sessionpresentation.PanelView{ID: 1, Fields: []sessionpresentation.StatusField{{Group: "Session", Value: "value"}}}}
	var panel StatusPanel
	panel.Update(v)
	panel.Render(new(terminalui.Painter), 80, 24)
	panel.Key("\x1b[B")
	v.Version.Revision++
	panel.Update(v)
	if panel.Top != 1 {
		t.Fatal("refresh reset navigation")
	}
	action, clear := panel.Key("\x1b")
	if !clear || action.Kind != sessionpresentation.PanelClose || action.Version != v.Version || action.Panel != v.Panel.ID {
		t.Fatalf("close target: %+v", action)
	}
	// A delayed action retains its originating identity after replacement.
	v.Panel = &sessionpresentation.PanelView{ID: 2}
	panel.Update(v)
	if panel.Top != 0 || panel.Rect != (Rect{}) || action.Panel != 1 {
		t.Fatal("panel replacement kept terminal state or rebound an action")
	}
	panel.Top = 7
	v.Version.Attachment++
	panel.Update(v)
	if panel.Top != 0 {
		t.Fatal("replacement attachment reused navigation")
	}
	panel.Update(sessionpresentation.View{})
	if panel.Open() {
		t.Fatal("closed publication left panel visible")
	}
}
