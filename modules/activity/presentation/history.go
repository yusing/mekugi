package presentation

import "slices"

// InsertHistory moves newly appended records before an existing source. Source
// identities, parsed values and output ownership remain attached to each record.
// A missing anchor appends the page after the retained history.
// Source: apps/mekugi/internal/router/app_server_history_pages.go:412:434@eaad597b.
func (m *Model[S, A]) InsertHistory(before, anchor uint64) {
	var retained, added []int
	at := -1
	for i, entry := range m.Entries {
		seq := entry.Source.ActivityFacts().Seq
		if seq > before {
			added = append(added, i)
			continue
		}
		if seq == anchor && at < 0 {
			at = len(retained)
		}
		retained = append(retained, i)
	}
	if at < 0 {
		at = len(retained)
	}
	m.Reorder(slices.Insert(retained, at, added...))
}
