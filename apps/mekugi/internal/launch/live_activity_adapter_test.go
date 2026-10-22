package launch

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
func (v *activityAdapter) Apply(event sessionpresentation.ActivityEvent) {
	v.ActivitySource.Apply(event)
	v.publishActivity()
}

func (v *activityAdapter) displayingChild() bool { return v.display.DisplayingChild() }
func (v *activityAdapter) displayTarget(caller string) sessionpresentation.Thread {
	return v.display.ActivityTarget(caller, v.ActivitySource)
}

func (u *appServerUI) testActivity(view *activityui.View) *activityAdapter {
	fixture := &activityAdapter{ActivitySource: activitySource(&u.attached, u.shell, view), View: view, display: u.attached.presentation}
	if view == u.shell.Agents {
		fixture.mainView = &activityAdapter{ActivitySource: u.attached.source, View: u.shell.Transcript, display: fixture.display}
	}
	return fixture
}

// bind supplies the actual application source boundary for standalone dialog tests.
func (v *activityAdapter) bind(terminal *terminalUI) *activityui.View {
	if terminal.main == nil {
		terminal.main = appServerTestFeatures(&appServerUI{activity: new(activitymodel.Service), shell: &terminalsession.Attachment{Transcript: v.View}, attached: attachedPresentation{executionPresentation: &executionPresentation{source: v.ActivitySource}}})
	} else if activitySource(&terminal.main.attached, terminal.main.shell, v.View) == nil {
		terminal.main.shell.Transcript, terminal.main.attached.source = v.View, v.ActivitySource
	}
	return v.View
}

func (v *activityAdapter) insertHistory(before uint64, belongs func(sessionpresentation.ActivityEntry) bool) {
	terminal := &terminalUI{Attachment: &terminalsession.Attachment{}}
	v.bind(terminal)
	insertActivityHistory(&terminal.main.attached, terminal.main.shell, v.ActivitySource, v.View, before, belongs)
}
