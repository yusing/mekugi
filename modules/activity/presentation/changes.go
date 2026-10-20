package presentation

// Source: modules/activity/terminalview/changes.go:20:21,136:151@[9822b997].
// ChangeRowsShown bounds the change rows open output shows.
const ChangeRowsShown = 12

// ChangeTotals sums a row set's known line counts. A footer that states
// totals, as a commit's does, is authoritative over the rows it summarizes.
func ChangeTotals(rows []ChangeRow) (added, removed int) {
	for _, row := range rows {
		if row.Footer && (row.Added != 0 || row.Removed != 0) {
			return row.Added, row.Removed
		}
	}
	for _, row := range rows {
		if !row.Footer {
			added, removed = added+max(0, row.Added), removed+max(0, row.Removed)
		}
	}
	return added, removed
}
