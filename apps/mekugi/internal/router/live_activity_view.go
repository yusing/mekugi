package router

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

// activityAdapter joins original host evidence to the Activity owners. It never paints.
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
