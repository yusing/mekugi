package router

import (
	"context"
	"slices"

	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

// liveActivityRecord keeps the source and its presentation together through
// replacement, retention and history reordering. revision changes whenever the
// presentation changes, including updates that do not reparse the source.
type liveActivityRecord = activitypresentation.Record[activityPaneEntry]

func (v *liveActivityView) appendEntry(entry activityPaneEntry, blocks []activityui.Block) {
	v.Model.Append(entry, v.disclosure.Keep(entry.Seq, blocks))
}
func (v *liveActivityView) replaceEntry(index int, entry activityPaneEntry, blocks []activityui.Block) {
	v.Model.Replace(index, entry, v.disclosure.Keep(entry.Seq, blocks))
	v.invalidateRuns(entry.Seq)
}
func (v *liveActivityView) mutateEntry(index int, mutate func(*liveActivityRecord)) {
	v.Model.Mutate(index, mutate)
	v.invalidateRuns(v.Entries[index].Source.Seq)
}
func (v *liveActivityView) removeEntries(from, to int) {
	for _, record := range v.Entries[from:to] {
		v.disclosure.Remove(record.Source.Seq)
	}
	v.Model.Remove(from, to)
	v.runs = nil
}
func (v *liveActivityView) blocks(index int) []activityui.Block {
	return v.disclosure.Blocks(v.Entries[index].Values)
}

// invalidateEntry keeps unrelated completed runs warm while one item streams.
// Main's later items can quote earlier assignments or continue reasoning, so
// invalidate its suffix as well. Grouping keys account for later traffic.
func (v *liveActivityView) invalidateEntry(seq uint64) {
	if i := slices.IndexFunc(v.Entries, func(record liveActivityRecord) bool { return record.Source.Seq == seq }); i >= 0 {
		v.Model.Revise(i)
		v.invalidateRuns(seq)
		return
	}
	v.invalidateRuns(seq)
}

func (v *liveActivityView) invalidateRuns(seq uint64) {
	if v.historyOrder {
		v.runs = nil
		return
	}
	for key := range v.runs {
		if seq <= key.last && (v.conversation || key.first <= seq) {
			delete(v.runs, key)
		}
	}
}

// reorderEntries moves whole records; sequence IDs and parsed annotations stay
// attached to the source they describe.
func (v *liveActivityView) reorderEntries(indices []int) {
	v.Model.Reorder(indices)
	v.runs = nil
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
	state := u.session.activity.Model[activitypresentation.Session[activityPaneEntry, activityPaneAgent]](&u.session.state)
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
