package router

import (
	"context"

	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

// liveActivityRecord keeps the source and its presentation together through
// replacement, retention and history reordering. revision changes whenever the
// presentation changes, including updates that do not reparse the source.
type liveActivityRecord = activitypresentation.Record[activityPaneEntry]

func (v *activityAdapter) appendEntry(entry activityPaneEntry, blocks []activityui.Block) {
	v.Model.Append(entry, activityui.Values(blocks))
}
func (v *activityAdapter) replaceEntry(index int, entry activityPaneEntry, blocks []activityui.Block) {
	v.Model.Replace(index, entry, activityui.Values(blocks))
}
func (v *activityAdapter) sourceBlocks(index int) []activityui.Block {
	return activityui.Disclosure(nil).Blocks(v.Entries[index].Values)
}

// Parsed blocks are ephemeral. The model attaches canonical membership to their values.
func identifyActivityBlocks(blocks []activityui.Block, source uint64) {
	values := activityui.Values(blocks)
	activitypresentation.Identify(values, source)
	for i := range blocks {
		blocks[i].Value = values[i]
	}
}

// attachActivity transfers the initial adapter feed into the composed attachment.
// Later frames retain the same model pointers; changing attachments drops both feeds.
func (u *appServerUI) attachActivity() {
	state := u.session.activity.Model[activitypresentation.Session[activityPaneEntry, activityPaneAgent]](u.session.Scope())
	if state == nil {
		return
	}
	if state.Commits == nil && u.replay == nil {
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
