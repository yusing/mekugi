package router

import (
	"context"
	"slices"

	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
)

// liveActivityRecord keeps the source and its presentation together through
// replacement, retention and history reordering. revision changes whenever the
// presentation changes, including updates that do not reparse the source.
type liveActivityRecord = activitypresentation.Record[activityPaneEntry]

func (v *activityAdapter) sourceValues(index int) []activitypresentation.Value {
	return slices.Clone(v.Entries[index].Values)
}

// attachActivity transfers the initial adapter feed into the composed attachment.
// Later frames retain the same model pointers; changing attachments drops both feeds.
func (u *appServerUI) attachActivity() {
	state := activitypresentation.Attached[activityPaneEntry, activityPaneAgent](u.session.Scope())
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
