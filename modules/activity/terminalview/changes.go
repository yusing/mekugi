package terminalview

import (
	"slices"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/terminalui"
	"github.com/yusing/mekugi/modules/activity/presentation"
)

// ChangeRow is one row of a change history read, such as `mchanges --list`
// or `--summary`, or of a version-control read or commit, laid out like
// confirmed edit rows.
type ChangeRow = presentation.ChangeRow

// Hash is the gold VCS output uses for commit hashes and branches.
const Hash = "\x1b[38;5;179m"

// ChangeRowsShown bounds the change rows open output shows.
const ChangeRowsShown = 12

// changeRows lays out change history as edit rows: a verb column, paths or
// IDs in one column, counts in the next, and bars scaled against the largest
// row. A label column too wide for its row narrows so the counts stay in view.
func changeRows(rows []ChangeRow, padding string, width int) []string {
	var footers []ChangeRow
	rows = slices.DeleteFunc(slices.Clone(rows), func(row ChangeRow) bool {
		if row.Footer {
			footers = append(footers, row)
		}
		return row.Footer
	})
	shown := rows[:min(len(rows), ChangeRowsShown)]
	verbs, labels, counts, scale := 0, 0, 0, 0
	for _, row := range shown {
		if row.Verb != "" {
			verbs = max(verbs, ansi.StringWidth(row.Verb)+1)
		}
		if row.Code != "" {
			verbs = max(verbs, 3)
		}
		labels = max(labels, ansi.StringWidth(changePlainLabel(row)))
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
			color := terminalui.Green
			switch row.Verb {
			case "Conflict":
				color = terminalui.Red
			case "?":
				color = terminalui.Amber
			}
			verb, cells := "", ansi.StringWidth(row.Verb)
			switch {
			case row.Code != "":
				verb, cells = statusCode(row.Code), ansi.StringWidth(row.Code)
			case row.Verb != "":
				verb = color + "\x1b[1m" + row.Verb + terminalui.Reset
			}
			line += verb + strings.Repeat(" ", max(0, verbs-cells))
		}
		label := changeLabel(row, column)
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
				line += " " + terminalui.Dim + row.Note + terminalui.Undim
			}
		case row.Note != "":
			line += gap + terminalui.Dim + row.Note + terminalui.Undim
		}
		lines = append(lines, padding+ansi.Truncate(strings.TrimRight(line, " "), width, "…"))
	}
	if more := len(rows) - len(shown); more > 0 {
		lines = append(lines, padding+Elision{Hidden: more, Unit: "more"}.String())
	}
	for _, footer := range footers {
		var parts []string
		if footer.Label != "" {
			parts = append(parts, Hash+footer.Label+"\x1b[39m")
		}
		if footer.Note != "" {
			parts = append(parts, terminalui.Dim+footer.Note+terminalui.Undim)
		}
		if plain := editCounts(footer.Added, footer.Removed); plain != "" {
			parts = append(parts, countText(plain))
		}
		lines = append(lines, padding+ansi.Truncate(strings.Join(parts, " "), width, "…"))
	}
	return lines
}

// statusCode colors a two-cell VCS status as git does: the staged state
// green, the unstaged state red, and untracked or conflicted paths red.
func statusCode(code string) string {
	var out strings.Builder
	conflict := strings.ContainsAny(code, "U?!C") || code == "AA" || code == "DD"
	for i, r := range code {
		switch {
		case r == ' ':
			out.WriteByte(' ')
			continue
		case i == 0 && !conflict:
			out.WriteString(terminalui.Green)
		default:
			out.WriteString(terminalui.Red)
		}
		out.WriteRune(r)
		out.WriteString("\x1b[39m")
	}
	return out.String()
}

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

func changePlainLabel(r ChangeRow) string {
	if r.From != "" {
		return r.From + " → " + r.Label
	}
	return r.Label
}

// label shows a file with path emphasis, eliding it to fit width, and a
// change ID, or a row with neither verb nor status, as it is.
func changeLabel(r ChangeRow, width int) string {
	switch {
	case r.Verb == "" && r.Code == "":
		return r.Label
	case r.From != "":
		from := fitPath(r.From, (width-3)/2)
		return terminalui.StyledPath(from) + terminalui.Dim + " → " + terminalui.Undim + terminalui.StyledPath(fitPath(r.Label, width-3-ansi.StringWidth(from)))
	}
	return terminalui.StyledPath(fitPath(r.Label, width))
}

// countText colors a row's added and removed line counts as edit rows do.
func countText(plain string) string {
	if plain == "" {
		return ""
	}
	if strings.HasPrefix(plain, "-") {
		return terminalui.Red + plain + "\x1b[39m"
	}
	return strings.NewReplacer("+", terminalui.Green+"+", " -", "\x1b[39m "+terminalui.Red+"-").Replace(plain) + "\x1b[39m"
}
