package activity

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

type markdownTable struct {
	rows  [][]string
	align []byte
}

// Table recognition requires a complete delimiter row. Until it arrives,
// streaming text stays ordinary Markdown rather than guessing at pipe syntax.
func parseMarkdownTable(lines []string) (markdownTable, int) {
	if len(lines) < 2 {
		return markdownTable{}, 0
	}
	header, ok := markdownTableCells(lines[0])
	if !ok {
		return markdownTable{}, 0
	}
	delimiter, ok := markdownTableCells(lines[1])
	if !ok || len(header) != len(delimiter) {
		return markdownTable{}, 0
	}
	table := markdownTable{rows: [][]string{header}, align: make([]byte, len(header))}
	for i, cell := range delimiter {
		core := strings.TrimSuffix(strings.TrimPrefix(cell, ":"), ":")
		if len(core) < 3 || strings.Trim(core, "-") != "" {
			return markdownTable{}, 0
		}
		if strings.HasSuffix(cell, ":") {
			table.align[i] = 'r'
			if strings.HasPrefix(cell, ":") {
				table.align[i] = 'c'
			}
		}
	}
	consumed := 2
	for _, line := range lines[2:] {
		cells, ok := markdownTableCells(line)
		if !ok {
			break
		}
		row := make([]string, len(header))
		copy(row, cells)
		table.rows = append(table.rows, row)
		consumed++
	}
	return table, consumed
}

// Optional outer pipes are structural; escaped pipes and code spans are content.
func markdownTableCells(line string) ([]string, bool) {
	line = strings.TrimSpace(line)
	_, fence := FenceDelimiter(line)
	if line == "" || strings.HasPrefix(line, ">") || fence ||
		strings.HasPrefix(line, "#") || strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* ") {
		return nil, false
	}
	var cells []string
	var cell strings.Builder
	pipes := 0
	trailing := false
	for i := 0; i < len(line); {
		if line[i] == '\\' && i+1 < len(line) {
			if line[i+1] != '|' {
				cell.WriteByte(line[i])
			}
			cell.WriteByte(line[i+1])
			i += 2
			trailing = false
			continue
		}
		if _, end, ok := liveActivityCodeSpan(line, i); ok {
			cell.WriteString(strings.ReplaceAll(line[i:end], `\|`, "|"))
			i = end
			trailing = false
			continue
		}
		if line[i] == '|' {
			if i != 0 {
				cells = append(cells, strings.TrimSpace(cell.String()))
			}
			cell.Reset()
			pipes++
			trailing = true
		} else {
			cell.WriteByte(line[i])
			trailing = false
		}
		i++
	}
	if !trailing {
		cells = append(cells, strings.TrimSpace(cell.String()))
	}
	return cells, pipes > 0 && len(cells) > 0
}

func (p *Painter) markdownTable(table markdownTable, width int) []string {
	n := len(table.align)
	widths := make([]int, n)
	styled := make([][]string, len(table.rows))
	total := 3*n + 1 // outside borders, separators, and cell padding
	for r, row := range table.rows {
		styled[r] = make([]string, n)
		for c, cell := range row {
			styled[r][c] = p.Inline(cell)
			if r == 0 {
				styled[r][c] = "\x1b[1m" + styled[r][c] + Reset
			}
			widths[c] = max(widths[c], min(width, ansi.StringWidth(styled[r][c])), 1)
		}
	}
	// Keep at least four display cells per column (or its natural width).
	// Below that, labeled records preserve the data without a clipped grid.
	minimum := total
	for _, w := range widths {
		minimum += min(w, 4)
	}
	if minimum > width {
		return p.markdownTableRecords(styled, width)
	}
	for _, w := range widths {
		total += w
	}
	for total > width {
		largest := 0
		for c := range widths {
			if widths[c] > widths[largest] {
				largest = c
			}
		}
		widths[largest]--
		total--
	}
	border := func(left, joint, right string) string {
		parts := make([]string, n)
		for c, w := range widths {
			parts[c] = strings.Repeat("─", w+2)
		}
		return Dim + left + strings.Join(parts, joint) + right + Undim
	}
	lines := []string{border("┌", "┬", "┐")}
	for r, row := range styled {
		wrapped := make([][]string, n)
		height := 1
		for c, cell := range row {
			wrapped[c] = tableCellLines(cell, widths[c])
			height = max(height, len(wrapped[c]))
		}
		for y := range height {
			var line strings.Builder
			line.WriteString(Dim + "│" + Undim)
			for c := range n {
				part := ""
				if y < len(wrapped[c]) {
					part = wrapped[c][y]
				}
				padding := max(0, widths[c]-ansi.StringWidth(part))
				left := 0
				switch table.align[c] {
				case 'r':
					left = padding
				case 'c':
					left = padding / 2
				}
				line.WriteString(" " + strings.Repeat(" ", left))
				line.WriteString(part)
				line.WriteString(strings.Repeat(" ", padding-left) + " " + Dim + "│" + Undim)
			}
			lines = append(lines, line.String())
		}
		if r == 0 {
			lines = append(lines, border("├", "┼", "┤"))
		}
	}
	return append(lines, border("└", "┴", "┘"))
}

func (p *Painter) markdownTableRecords(rows [][]string, width int) []string {
	var lines []string
	if len(rows) == 1 {
		for _, cell := range rows[0] {
			lines = append(lines, tableCellLines(cell, width)...)
		}
		return lines
	}
	labelWidth := 0
	for _, header := range rows[0] {
		labelWidth = max(labelWidth, ansi.StringWidth(header)+2)
	}
	for r, row := range rows[1:] {
		if r > 0 {
			lines = append(lines, Dim+strings.Repeat("─", width)+Undim)
		}
		for c, cell := range row {
			label := "\x1b[1m" + rows[0][c] + Reset + ": "
			if labelWidth > width/2 {
				lines = append(lines, tableCellLines(strings.TrimSpace(label), width)...)
				lines = append(lines, tableCellLines(cell, width)...)
			} else {
				for i, part := range tableCellLines(cell, width-labelWidth) {
					prefix := strings.Repeat(" ", labelWidth)
					if i == 0 {
						prefix = label + strings.Repeat(" ", labelWidth-ansi.StringWidth(label))
					}
					lines = append(lines, prefix+part)
				}
			}
		}
	}
	return lines
}

// A viewport paints rows independently. Close links at every cell boundary and
// restore them on continuations, so neither borders nor adjacent cells link.
func tableCellLines(cell string, width int) []string {
	rows := Wrap(cell, width, false)
	link := ""
	const open = "\x1b]8;;"
	const end = "\x1b\\"
	for i, row := range rows {
		prefix := link
		rest := row
		for {
			_, after, ok := strings.Cut(rest, open)
			if !ok {
				break
			}
			target, after, ok := strings.Cut(after, end)
			if !ok {
				break
			}
			link = ""
			if target != "" {
				link = open + target + end
			}
			rest = after
		}
		rows[i] = prefix + ansi.Truncate(row, width, "…") + Reset + open + end
	}
	return rows
}
