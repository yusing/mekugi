package router

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

// activityAdapter is a consuming fixture for a separately owned source and painter.
type activityAdapter struct {
	*sessionpresentation.ActivitySource
	*activityui.View
	display  *sessionpresentation.Model
	mainView *activityAdapter
}

const liveActivityFeedLimit = activitypresentation.FeedLimit

func newTranscriptAdapter(view *activityui.View, audience sessionpresentation.ActivityAudience) *activityAdapter {
	v := &activityAdapter{ActivitySource: &sessionpresentation.ActivitySource{Audience: audience, Coverage: "CONNECTING", Model: new(activitypresentation.Model[sessionpresentation.ActivityEntry, sessionpresentation.ActivityAgent])}, View: view, display: new(sessionpresentation.Model)}
	v.display.Attach(sessionpresentation.Thread{ID: "/root", Caller: "/root"}, false)
	return v
}

// Apply delivers the admitted source revision before terminal input or animation
// can consult its selection and caches. The source reducer itself never paints.
func (v *activityAdapter) Apply(event sessionpresentation.ActivityEvent) bool {
	ended := v.ActivitySource.Apply(event)
	v.publishActivity()
	return ended
}

func (v *activityAdapter) displayingChild() bool { return v.display.DisplayingChild() }
func (v *activityAdapter) displayTarget(caller string) sessionpresentation.Thread {
	return v.display.ActivityTarget(caller, v.ActivitySource)
}

func (u *appServerUI) testActivity(view *activityui.View) *activityAdapter {
	fixture := &activityAdapter{ActivitySource: u.activitySource(view), View: view, display: u.sessionPresentation()}
	if view == u.agents {
		fixture.mainView = &activityAdapter{ActivitySource: u.source, View: u.view, display: fixture.display}
	}
	return fixture
}

// bind supplies the actual application source boundary for standalone dialog tests.
func (v *activityAdapter) bind(terminal *terminalUI) *activityui.View {
	if terminal.main == nil {
		terminal.main = appServerTestFeatures(&appServerUI{activity: new(activitymodel.Service), view: v.View, source: v.ActivitySource})
	} else if terminal.main.activitySource(v.View) == nil {
		terminal.main.view, terminal.main.source = v.View, v.ActivitySource
	}
	return v.View
}

func (v *activityAdapter) insertHistory(before uint64, belongs func(sessionpresentation.ActivityEntry) bool) {
	terminal := &terminalUI{Attachment: &terminalsession.Attachment{}}
	v.bind(terminal)
	terminal.main.insertActivityHistory(v.ActivitySource, v.View, before, belongs)
}
