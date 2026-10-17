package router

import (
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

func (v *activityAdapter) publishActivity() {
	var source *sessionpresentation.ActivitySource
	var view *activityui.View
	if v.mainView != nil {
		source, view = v.mainView.ActivitySource, v.mainView.View
	}
	deliverActivity(v.View, v.ActivitySource, v.display, source, view)
}

func (v *activityAdapter) Render(width, height int, now time.Time) []string {
	v.publishActivity()
	return renderActivity(v.ActivitySource, v.View, width, height, now)
}

func (v *activityAdapter) RenderFeed(width, rows int) activityui.Feed {
	v.publishActivity()
	return renderActivityFeed(v.ActivitySource, v.View, width, rows)
}

func (v *activityAdapter) NativeRoster(width, limit int, now time.Time, focused bool) []string {
	v.publishActivity()
	return v.View.NativeRoster(width, limit, now, focused)
}

func (v *activityAdapter) takeDisplayIntent() *sessionpresentation.DisplayIntent {
	intent := v.View.DisplayRequest
	if intent == nil {
		return nil
	}
	v.View.DisplayRequest = nil
	return &sessionpresentation.DisplayIntent{
		Version: sessionpresentation.Version{Attachment: intent.Attachment, Revision: intent.Revision},
		Thread:  v.displayTarget(intent.Caller),
	}
}

func settleActivity(now time.Time, adapters ...*activityAdapter) bool {
	views := make([]*activityui.View, len(adapters))
	for i, adapter := range adapters {
		adapter.publishActivity()
		views[i] = adapter.View
	}
	return activityui.Settle(now, views...)
}
