package terminalsession

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/libraries/terminalui"
	"testing"
)

func TestResumePickerDetachedNavigationAndPageDemand(t *testing.T) {
	v := sessionpresentation.View{Version: sessionpresentation.Version{Attachment: 1, Revision: 1}, Resume: &sessionpresentation.ResumeView{ID: 1, CanPage: true, PageAfter: "second", Rows: []sessionpresentation.ResumeRow{{ID: "first"}, {ID: "second"}}}}
	var p ResumePicker
	p.Update(v)
	p.Render(new(terminalui.Painter), 80, 30, "")
	p.Key("\x1b[B")
	action := p.Key("\r")
	if action.Kind != sessionpresentation.ResumeChoose || action.Thread != "second" || action.Version != v.Version {
		t.Fatalf("selection: %+v", action)
	}
	page := p.PageDemand()
	if page.Kind != sessionpresentation.ResumePage || page.After != "second" {
		t.Fatalf("page demand: %+v", page)
	}
	old := *v.Resume
	v.Resume = &old
	v.Resume.Query = "search"
	v.Version.Revision++
	p.Update(v)
	if p.Selected != 0 || p.Viewport == 0 {
		t.Fatal("query reset lost terminal capacity or retained cursor")
	}
	clear := p.Key("\x1b")
	if clear.Kind != sessionpresentation.ResumeSearch || clear.Query != "" {
		t.Fatal("Escape did not clear search first")
	}
	v.Resume.Loading, v.Resume.CanPage = true, false
	p.Update(v)
	if p.PageDemand().Kind != sessionpresentation.ResumeNoAction {
		t.Fatal("loading generated another demand")
	}
	p.Update(sessionpresentation.View{})
	if p.Open() {
		t.Fatal("closed publication retained chooser")
	}
}
