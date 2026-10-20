package terminalsession

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
	"testing"
	"time"
)

func TestSideDockFollowsExchangeIdentity(t *testing.T) {
	v := sessionpresentation.View{Version: sessionpresentation.Version{Attachment: 1, Revision: 1}, Side: &conversationmodel.SideView{ID: 1, Question: "question", Answer: []conversationmodel.Answer{{Text: "answer"}}}}
	var dock SideDock
	dock.Update(v)
	dock.Render(new(activityui.Painter), time.Unix(1, 0), 60, 10)
	if handled, _ := dock.Key("\x1b[5~"); !handled || dock.Scroll == 0 {
		t.Fatal("page key did not scroll the terminal dock")
	}
	v.Version.Revision++
	dock.Update(v)
	if dock.Scroll == 0 {
		t.Fatal("refresh reset scrolling")
	}
	handled, close := dock.Key("\x1b")
	if !handled || close == nil || close.Exchange != 1 || close.Version != v.Version {
		t.Fatal("close lost source identity")
	}
	next := *v.Side
	next.ID++
	v.Side = &next
	dock.Update(v)
	if dock.Scroll != 0 || len(dock.CopyAnswer) != 0 {
		t.Fatal("follow-up retained old navigation or copy source")
	}
	dock.Update(sessionpresentation.View{})
	if dock.Open() {
		t.Fatal("closed publication retained side dock")
	}
}
