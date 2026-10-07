package router

import (
	"slices"

	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

// liveActivityRecord keeps the source and its presentation together through
// replacement, retention and history reordering. revision changes whenever the
// presentation changes, including updates that do not reparse the source.
type liveActivityRecord struct {
	activityPaneEntry
	blocks   []activityui.Block
	revision uint64
}

func (v *liveActivityView) appendEntry(entry activityPaneEntry, blocks []activityui.Block) {
	v.revision++
	v.entries = append(v.entries, liveActivityRecord{entry, blocks, v.revision})
}

func (v *liveActivityView) replaceEntry(index int, entry activityPaneEntry, blocks []activityui.Block) {
	v.entries[index] = liveActivityRecord{activityPaneEntry: entry, blocks: blocks}
	v.reviseEntry(index)
}

// mutateEntry keeps in-place source and annotation edits inside the same
// ownership boundary as replacement, including their cache revision.
func (v *liveActivityView) mutateEntry(index int, mutate func(*liveActivityRecord)) {
	mutate(&v.entries[index])
	v.reviseEntry(index)
}

func (v *liveActivityView) removeEntries(from, to int) {
	v.entries = slices.Delete(v.entries, from, to)
	v.runs, v.skills = nil, nil
}

func (v *liveActivityView) runRevision(from, to int) uint64 {
	var revision uint64
	for _, record := range v.entries[from:to] {
		revision = max(revision, record.revision)
	}
	return revision
}

// invalidateEntry keeps unrelated completed runs warm while one item streams.
// Main's later items can quote earlier assignments or continue reasoning, so
// invalidate its suffix as well. Grouping keys account for later traffic.
func (v *liveActivityView) invalidateEntry(seq uint64) {
	if i := slices.IndexFunc(v.entries, func(record liveActivityRecord) bool { return record.Seq == seq }); i >= 0 {
		v.reviseEntry(i)
		return
	}
	v.invalidateRuns(seq)
}

func (v *liveActivityView) reviseEntry(index int) {
	v.revision++
	v.entries[index].revision = v.revision
	v.invalidateRuns(v.entries[index].Seq)
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
	records := slices.Clone(v.entries)
	for i, old := range indices {
		v.entries[i] = records[old]
	}
	v.runs, v.skills = nil, nil
}
