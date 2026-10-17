package router

import (
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

func (v *activityAdapter) publishActivity() {
	var main *sessionpresentation.ActivitySource
	if v.mainView != nil {
		main = v.mainView.ActivitySource
	}
	publication := v.ActivitySource.Publication(v.display, main)

	for i, entry := range publication.Entries {
		if frame, needed := v.View.CommandFrame(entry.Seq, entry.Revision); needed {
			v.View.UpdateCommandFrame(commandFrame(v.Entries[i], frame))
		}
	}
	if v.Audience != sessionpresentation.MainAudience {
		v.View.StatusLabel = v.Coverage
	}
	if v.mainView != nil {
		v.View.MainSummary = v.mainView.View.SummaryBlocks(publication.MainSummary)
		v.View.MainStatus = v.mainView.View.StatusLabel
	}
	display := v.display.View()
	v.View.Update(publication, activityui.Display{Caller: display.Displayed.Caller, Attachment: display.Version.Attachment, Revision: display.Version.Revision})
}

func (v *activityAdapter) Render(width, height int, now time.Time) []string {
	v.publishActivity()
	for {
		lines := v.View.Render(width, height, now)
		demands := v.View.OutputDemands()
		if len(demands) == 0 {
			return lines
		}
		v.View.Hydrate(v.ResolveOutputDemands(demands))
	}
}

func (v *activityAdapter) RenderFeed(width, rows int) activityui.Feed {
	v.publishActivity()
	for {
		feed := v.View.RenderFeed(width, rows)
		demands := v.View.OutputDemands()
		if len(demands) == 0 {
			return feed
		}
		v.View.Hydrate(v.ResolveOutputDemands(demands))
	}
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
