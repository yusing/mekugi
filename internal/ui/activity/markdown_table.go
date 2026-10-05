package activity

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

type markdownTable struct {
	rows   [][]string
	align  []byte
	copyID uint64
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
	var copies [][]CopyFragment
	if p.CopySource && !p.LayoutOnly {
		copies = make([][]CopyFragment, len(table.rows))
	}
	identity := table.copyID
	alignment := string(table.align)
	total := 3*n + 1 // outside borders, separators, and cell padding
	for r, row := range table.rows {
		styled[r] = make([]string, n)
		if copies != nil {
			copies[r] = make([]CopyFragment, n)
		}
		for c, cell := range row {
			styled[r][c] = p.Inline(cell)
			if copies != nil {
				f := copyInline(cell)
				f.ID = copyID(cell, identity+uint64(r*n+c))
				f.Table, f.Row, f.Cell, f.Align = identity, r, c, alignment
				f.Hard = false
				copies[r][c] = f
			}
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
		return p.markdownTableRecords(styled, copies, width)
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
		row := Dim + left + strings.Join(parts, joint) + right + Undim
		if p.CopySource && !p.LayoutOnly {
			row = copyTag(CopyFragment{Table: identity, Align: alignment, Rule: true, Width: ansi.StringWidth(row)}) + row
		}
		return row
	}
	lines := []string{border("┌", "┬", "┐")}
	for r, row := range styled {
		wrapped := make([][]string, n)
		height := 1
		for c, cell := range row {
			wrapped[c] = p.copyTableCell(tableCellLines(cell, widths[c]), copies, r, c)
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

func (p *Painter) markdownTableRecords(rows [][]string, copies [][]CopyFragment, width int) []string {
	var lines []string
	if len(rows) == 1 {
		for c, cell := range rows[0] {
			lines = append(lines, p.copyTableCell(tableCellLines(cell, width), copies, 0, c)...)
		}
		return lines
	}
	labelWidth := 0
	for _, header := range rows[0] {
		labelWidth = max(labelWidth, ansi.StringWidth(header)+2)
	}
	for r, row := range rows[1:] {
		if r > 0 {
			rule := Dim + strings.Repeat("─", width) + Undim
			if p.CopySource && !p.LayoutOnly {
				f := copies[0][0]
				f.Rule, f.Width = true, width
				rule = copyTag(f) + rule
			}
			lines = append(lines, rule)
		}
		for c, cell := range row {
			header := p.copyTableCell([]string{"\x1b[1m" + rows[0][c] + Reset}, copies, 0, c)[0]
			label := header + ": "
			if labelWidth > width/2 {
				lines = append(lines, p.copyTableCell(tableCellLines("\x1b[1m"+rows[0][c]+Reset+":", width), copies, 0, c)...)
				lines = append(lines, p.copyTableCell(tableCellLines(cell, width), copies, r+1, c)...)
			} else {
				for i, part := range p.copyTableCell(tableCellLines(cell, width-labelWidth), copies, r+1, c) {
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
	for i, row := range rows {
		rows[i] = ansi.Truncate(row, width, "…") + Reset + "\x1b]8;;\x1b\\"
	}
	return rows
}

func (p *Painter) copyTableCell(rows []string, copies [][]CopyFragment, row, column int) []string {
	if copies == nil {
		return rows
	}
	return p.CopyWrapped(rows, copies[row][column], 0)
}
