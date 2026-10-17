package router

import (
	"context"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
)

// attachActivity transfers the initial adapter feed into the composed attachment.
// Later frames retain the same model pointers; changing attachments drops both feeds.
func (u *appServerUI) attachActivity() {
	state := activitypresentation.Attached[sessionpresentation.ActivityEntry, sessionpresentation.ActivityAgent](u.session.Scope())
	if state.Commits == nil && u.session.activity != nil && u.replay == nil {
		ctx := u.ctx
		if ctx == nil {
			ctx = context.Background()
		}
		state.Commits = activitypresentation.NewCommitReader(ctx)
	}
	if u.commits != state.Commits {
		u.commits.Close()
		u.commits = state.Commits
	}
	if u.view != nil && u.view.Model != &state.Main {
		state.Main = *u.view.Model
		u.view.Model = &state.Main
	}
	if u.agents != nil && u.agents.Model != &state.Children {
		state.Children = *u.agents.Model
		u.agents.Model = &state.Children
	}
}
