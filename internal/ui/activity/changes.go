package activity

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// ChangeRow is one row of a change history read, such as `mchanges --list`
// or `--summary`, laid out like confirmed edit rows.
type ChangeRow struct {
	Verb           string // Edited, Created, Deleted, Moved, Conflict or ?; empty for a change ID.
	From           string // A moved file's earlier path.
	Label          string // File path or change ID range.
	Added, Removed int    // Known line counts; zero counts are omitted.
	Note           string // Muted detail after the counts, such as a status.
}

// ChangeRowsShown bounds the change rows open output shows.
const ChangeRowsShown = 12

// changeRows lays out change history as edit rows: a verb column, paths or
// IDs in one column, counts in the next, and bars scaled against the largest
// row. A label column too wide for its row narrows so the counts stay in view.
func changeRows(rows []ChangeRow, padding string, width int) []string {
	shown := rows[:min(len(rows), ChangeRowsShown)]
	verbs, labels, counts, scale := 0, 0, 0, 0
	for _, row := range shown {
		if row.Verb != "" {
			verbs = max(verbs, ansi.StringWidth(row.Verb)+1)
		}
		labels = max(labels, ansi.StringWidth(row.plainLabel()))
		counts = max(counts, len(editCounts(row.Added, row.Removed)))
		scale = max(scale, row.Added+row.Removed)
	}
	if len(shown) < 2 {
		scale = 0
	}
	room := width - verbs - 2 - counts
	if scale > 0 {
		room -= 1 + statBarCells
	}
	column := min(labels, max(16, room))
	var lines []string
	for _, row := range shown {
		line := ""
		if verbs > 0 {
			color := Green
			switch row.Verb {
			case "Conflict":
				color = Red
			case "?":
				color = Amber
			}
			verb := ""
			if row.Verb != "" {
				verb = color + "\x1b[1m" + row.Verb + Reset
			}
			line += verb + strings.Repeat(" ", verbs-ansi.StringWidth(row.Verb))
		}
		label := row.label(column)
		if ansi.StringWidth(label) > column {
			label = ansi.Truncate(label, column, "…")
		}
		line += label
		// A row without counts shows its note in their place.
		gap := strings.Repeat(" ", max(0, column-ansi.StringWidth(label))+2)
		switch plain := editCounts(row.Added, row.Removed); {
		case plain != "":
			line += gap + countText(plain)
			if bar := statBar(row.Added, row.Removed, scale); bar != "" {
				line += strings.Repeat(" ", counts-len(plain)+1) + bar
			}
			if row.Note != "" {
				line += " " + Dim + row.Note + Undim
			}
		case row.Note != "":
			line += gap + Dim + row.Note + Undim
		}
		lines = append(lines, padding+ansi.Truncate(strings.TrimRight(line, " "), width, "…"))
	}
	if more := len(rows) - len(shown); more > 0 {
		lines = append(lines, padding+Elision{Hidden: more, Unit: "more"}.String())
	}
	return lines
}

func (r ChangeRow) plainLabel() string {
	if r.From != "" {
		return r.From + " → " + r.Label
	}
	return r.Label
}

// label shows a file with path emphasis, eliding it to fit width, and a
// change ID as it is.
func (r ChangeRow) label(width int) string {
	switch {
	case r.Verb == "":
		return r.Label
	case r.From != "":
		from := fitPath(r.From, (width-3)/2)
		return Path(from) + Dim + " → " + Undim + Path(fitPath(r.Label, width-3-ansi.StringWidth(from)))
	}
	return Path(fitPath(r.Label, width))
}

// countText colors a row's added and removed line counts as edit rows do.
func countText(plain string) string {
	if plain == "" {
		return ""
	}
	if strings.HasPrefix(plain, "-") {
		return Red + plain + "\x1b[39m"
	}
	return strings.NewReplacer("+", Green+"+", " -", "\x1b[39m "+Red+"-").Replace(plain) + "\x1b[39m"
}
