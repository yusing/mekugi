package launch

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

// renderActivity services explicit output demands between detached render passes.
// The painter itself never publishes source state or opens retained output.
func renderActivity(source *sessionpresentation.ActivitySource, view *activityui.View, width, height int, now time.Time) []string {
	for {
		lines := view.Render(width, height, now)
		demands := view.OutputDemands()
		if len(demands) == 0 {
			return lines
		}
		view.Hydrate(source.ResolveOutputDemands(demands))
	}
}

func renderActivityFeed(source *sessionpresentation.ActivitySource, view *activityui.View, width, rows int) activityui.Feed {
	for {
		feed := view.RenderFeed(width, rows)
		demands := view.OutputDemands()
		if len(demands) == 0 {
			return feed
		}
		view.Hydrate(source.ResolveOutputDemands(demands))
	}
}

// openRequested opens an operation the view's pointer handling clicked.
func (u *terminalUI) openRequested(view *activityui.View) {
	if snippet := view.Opening; snippet != (activityui.Snippet{}) {
		view.Opening = activityui.Snippet{}
		if snippet.Block == activityui.EditNavigationSnippet {
			u.openActivityEdit(view, snippet.Run, snippet.Path)
		} else if !u.openOutput(view, snippet) {
			u.openEntry(view, snippet.Run)
		}
	}
}
