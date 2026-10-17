package router

import (
	"context"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
)

// attachActivity transfers the initial source feed into the composed attachment.
// Later frames retain the same model pointers; changing attachments drops both feeds.
func (u *appServerUI) attachActivity() {
	if u.source == nil {
		u.source = sessionpresentation.NewActivitySource(sessionpresentation.MainAudience)
	}
	if u.childSource == nil {
		u.childSource = sessionpresentation.NewActivitySource(sessionpresentation.ChildAudience)
		u.childSource.Coverage = "" // Live/replayed host observations, without collector connection state.
	}

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
	if u.source.Model != &state.Main {
		state.Main = *u.source.Model
		u.source.Model = &state.Main
	}
	if u.childSource.Model != &state.Children {
		state.Children = *u.childSource.Model
		u.childSource.Model = &state.Children
	}
}
