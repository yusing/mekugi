package sessionpresentation

import (
	"slices"

	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
)

// Source: apps/mekugi/internal/router/live_activity_record.go:10:13@45fbe279
// ActivityRecord keeps the source and its presentation together through
// replacement, retention and history reordering. revision changes whenever the
// presentation changes, including updates that do not reparse the source.
type ActivityRecord = activitypresentation.Record[ActivityEntry]

// Source: apps/mekugi/internal/router/live_activity_record.go:15:17@45fbe279
func (v *ActivitySource) SourceValues(index int) []activitypresentation.Value {
	return slices.Clone(v.Entries[index].Values)
}
