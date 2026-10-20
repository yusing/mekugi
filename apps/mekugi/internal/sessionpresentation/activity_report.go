package sessionpresentation

import activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"

// Source: apps/mekugi/internal/router/exec_track.go:208:229@e596fd5f.
// SettleCommandReport updates attribution on the existing completed record. This is
// separate from native merging, which must continue rejecting late host deltas.
func (v *ActivitySource) SettleCommandReport(enriched ActivityEntry, valid bool) {
	for i, record := range v.Entries {
		if !enriched.Native.SameItem(record.Source.Native) {
			continue
		}
		entry := record.Source
		native := *entry.Native
		native.Segments, native.SegmentsPending = nil, false
		entry.Native = &native
		values := record.Values
		if valid {
			native.Segments, native.Changes, native.Commit = enriched.Native.Segments, enriched.Native.Changes, enriched.Native.Commit
			entry.Completed = enriched.Completed
			entry.OutputTail, entry.OutputOmit = enriched.OutputTail, enriched.OutputOmit
			values = activitypresentation.RetainAnnotations(ActivityContent(entry).Values(), values)
		}
		v.Model.Replace(i, entry, values)
		return
	}
}
