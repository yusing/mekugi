package diffmodel

// Source: libraries/diffrender/diff_syntax.go:243:257@[ebde9ad5]
// AlignRows replaces caller-owned row text with its before/after value,
// preserving hunk geometry without allocating another row slice.
func AlignRows(rows []ReviewRow, before, after []string) {
	oldIndex, newIndex := 0, 0
	for i, row := range rows {
		if row.Kind != '+' {
			rows[i].Text = before[oldIndex]
			oldIndex++
		}
		if row.Kind != '-' {
			rows[i].Text = after[newIndex]
			newIndex++
		}
	}
}
