package terminalui

import (
	"math"
	"strconv"
	"strings"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	ansiparser "github.com/charmbracelet/x/ansi/parser"
	"github.com/yusing/mekugi/libraries/diffrender"
)

// Source: modules/activity/terminalview/dialog.go:23:44,296:539@563f0c37.
// DialogPage is a supplied source document for a terminal dialog. Lines stay unwrapped, so a long live stream costs a layout only
// for the rows in view.
type DialogPage struct {
	Title  string // Styled verb and target.
	Live   bool   // Output is still streaming.
	Detail string // Styled facts: line spans, line count, exit.
	Lines  []DialogLine
	Text   string // Plain output, or source without output, for copying.
	digits int    // Width of the line-number column.
}

// DialogLine is one numbered source/output line, a logical unnumbered error
// line, or a pre-laid-out row such as a note or rendered Markdown.
type DialogLine struct {
	Wrap        bool          // Keep an unnumbered logical line intact for search; wrap only for display.
	Number      int           // Zero leaves the number column blank.
	Gutter      string        // "│" before source, "┆" before output, empty for other rows.
	GutterStyle string        // Optional number style, including wrapped rows and their left padding.
	Text        string        // Styled.
	CopySource  *CopyFragment // Original output behind a generated file heading.
}

// Add retains a logical line and updates its shared number-column width.
func (d *DialogPage) Add(line DialogLine) {
	d.Lines = append(d.Lines, line)
	if line.Number > 0 {
		d.digits = max(d.digits, len(strconv.Itoa(line.Number)))
	}
}

// textWidth is the width numbered lines wrap within.
func (d DialogPage) textWidth(width int) int {
	if d.digits == 0 {
		return max(1, width)
	}
	return max(1, width-d.digits-3)
}

// Indent is the cells line i's rows spend on its number and gutter.
func (d DialogPage) Indent(i int) int {
	if d.Lines[i].Gutter == "" {
		return 0
	}
	return d.digits + 3
}

// RowCount is how many rows line i takes at width.
func (d DialogPage) RowCount(i, width int) int {
	line := d.Lines[i]
	if line.Gutter == "" {
		if line.Wrap {
			return wrappedRows(line.Text, max(1, width))
		}
		return 1 // Notes and Markdown rows are laid out already.
	}
	return wrappedRows(line.Text, d.textWidth(width))
}

// wrappedRows counts the rows Wrap's hard wrap gives text, without laying them
// out: a character that does not fit its row starts the next.
func wrappedRows(text string, width int) int {
	rows, column := 1, 0
	var state byte
	for len(text) > 0 {
		_, cells, n, next := ansi.DecodeSequence(text, state, nil)
		if cells > 0 {
			if column+cells > width {
				rows, column = rows+1, 0
			}
			column += cells
		}
		text, state = text[n:], next
	}
	return rows
}

// Rows lays out line i at width: numbered lines wrap under their gutter, and
// continuation rows leave the number blank.
func (d DialogPage) Rows(i, width int) []string {
	return d.RowsRange(i, width, 0, math.MaxInt)
}

// RowsRange materializes only the requested display rows of a logical line.
// Skipped source is scanned without retaining wrapped rows or copying its text.
func (d DialogPage) RowsRange(i, width, first, count int) []string {
	if count <= 0 {
		return nil
	}
	line := d.Lines[i]
	if line.Gutter == "" {
		if line.Wrap {
			return hardWrapWindow(line.Text, max(1, width), first, count)
		}
		if first > 0 {
			return nil
		}
		return []string{line.Text}
	}
	parts := hardWrapWindow(line.Text, d.textWidth(width), first, count)
	rows := make([]string, len(parts))
	for k, part := range parts {
		if line.CopySource != nil {
			fragment := *line.CopySource
			fragment.Width = ansi.StringWidth(part)
			part = AttachCopy(part, []CopySpan{{CopyFragment: fragment}})
		}
		number := ""
		if first+k == 0 && line.Number > 0 {
			number = strconv.Itoa(line.Number)
		}
		style, end := Dim, Undim
		gutter := line.Gutter
		if line.GutterStyle != "" {
			style, end = line.GutterStyle, Reset
			gutter = "\x1b[49m" + gutter
		}
		rows[k] = style + strings.Repeat(" ", d.digits-len(number)) + number + " " + gutter + end + " " + part
	}
	return rows
}

// hardWrapWindow locates source boundaries before using the shared wrapper, so
// style restoration and hyperlink handling remain identical to complete rows.
func hardWrapWindow(text string, width, first, count int) []string {
	var style uv.Style
	var link uv.Link
	parser := ansi.GetParser()
	defer func() { parser.SetHandler(ansi.Handler{}); ansi.PutParser(parser) }()
	parser.SetHandler(ansi.Handler{HandleCsi: func(cmd ansi.Cmd, params ansi.Params) {
		if cmd == 'm' {
			uv.ReadStyle(params, &style)
		}
	}, HandleOsc: func(cmd int, data []byte) {
		if cmd == 8 {
			uv.ReadLink(data, &link)
		}
	}})
	row, column, start := 0, 0, 0
	carry := ""
	leadingWide := false
	wrap := func(end int) []string {
		rows := Wrap(carry+text[start:end], width, true)
		if leadingWide {
			rows = rows[1:] // The full source already supplied this glyph's leading empty row.
		}
		return rows
	}
	var state byte
	for at := 0; at < len(text); {
		seq, cells, n, next := ansi.DecodeSequence(text[at:], state, nil)
		newline := seq == "\n"
		boundWidth := cells
		if cells == 0 && len(seq) == 1 {
			// Hardwrap checks one cell for executable controls without advancing
			// the column. Reuse its parser classification instead of a control list.
			if _, action := ansiparser.Table.Transition(ansiparser.GroundState, seq[0]); action == ansiparser.ExecuteAction {
				boundWidth = 1
			}
		}
		if newline || boundWidth > 0 && column+boundWidth > width {
			row++
			if row >= first && row-first >= count {
				return wrap(at)
			}
			column = 0
			if row == first {
				leadingWide = !newline && cells > width
				start = at
				if newline {
					start += n
				}
				carry = style.String()
				if style.IsZero() {
					carry = Reset
				}
				if !link.IsZero() {
					carry += ansi.SetHyperlink(link.URL, link.Params)
				}
			}
		}
		if row < first && strings.HasPrefix(seq, "\x1b") {
			parser.Parse([]byte(seq))
		}
		column += cells
		at, state = at+n, next
	}
	if row < first {
		return nil
	}
	return wrap(len(text))
}

// MatchRow finds a case-insensitive substring in a logical line and locates
// its first display row. Search must not lose text at visual wrap boundaries.
func (d DialogPage) MatchRow(i, width int, query string) (int, bool) {
	line := d.Lines[i]
	text := strings.ToLower(ansi.Strip(line.Text))
	at := strings.Index(text, strings.ToLower(query))
	if at < 0 {
		return 0, false
	}
	if line.Gutter != "" {
		width = d.textWidth(width)
	}
	if line.Wrap || line.Gutter != "" {
		for _, first := range text[at:] {
			return wrappedRows(text[:at]+string(first), max(1, width)) - 1, true
		}
	}
	return 0, true
}

// DialogFrame is one frame of the output dialog, before layout.
type DialogFrame struct {
	Tabs         string // Styled command selector, empty for a single result.
	Page         DialogPage
	Position     string   // Page position among a merged row's invocations, such as "2 / 4".
	Paused       bool     // Live output the reader scrolled away from.
	Rows         []string // Visible body rows.
	GutterStyles []string // Optional styles for visible rows' left padding.
	Top          int      // Index of the first visible body row, for the scroll thumb.
	Total        int      // Body rows in all.
	Footer       string   // Styled controls.
}

// Backdrop fades a screen row the dialog is drawn over: its colors and
// attributes give way to faint text, and cursor moves stay in place.
func Backdrop(row string) string {
	var out strings.Builder
	out.WriteString(Dim)
	var state byte
	for row != "" {
		seq, _, n, next := ansi.DecodeSequence(row, state, nil)
		if !strings.HasPrefix(seq, "\x1b[") || !strings.HasSuffix(seq, "m") {
			out.WriteString(seq)
		}
		row, state = row[n:], next
	}
	return out.String()
}

// DialogChrome is the rows a dialog frame takes beyond its body: the top
// edge, the detail row, its rule and the bottom edge.
const DialogChrome = 4

const DialogClose = "[×]"
const DialogCloseWidth = 3

// Dialog boxes a frame width by height: the title and page position on the
// top edge, the detail row and a rule, the body with a scroll thumb on its
// right edge, and controls on the bottom edge.
func (p *Painter) Dialog(f DialogFrame, width, height int) []string {
	if width < 12 || height < DialogChrome+1 {
		return nil
	}
	// Accent edges and an opaque surface separate the modal from the panes.
	edge := func(s string) string { return p.Theme.Accent() + s + Reset }
	inner := width - 4
	right := f.Position
	if f.Page.Live {
		live := Green + "● live" + Reset
		if f.Paused {
			live += Dim + " · paused" + Undim
		}
		right = strings.TrimSpace(live + " " + right)
	}
	if right != "" {
		right += " "
	}
	right += DialogClose
	right = " " + right + " "
	room := width - 6 - ansi.StringWidth(right)
	if room < 1 {
		right, room = " "+DialogClose+" ", width-8-DialogCloseWidth
	}
	title := ansi.Truncate(f.Page.Title, room, "…") + Reset
	fill := max(0, width-6-ansi.StringWidth(title)-ansi.StringWidth(right))
	lines := []string{edge("╭─ ") + title + " " + edge(strings.Repeat("─", fill)) + right + edge("─╮")}
	row := func(text string, thumb bool, gutterStyle string) string {
		text = ansi.Truncate(text, inner, "…")
		closing := edge("│")
		if thumb {
			closing = p.Theme.Accent() + "▌" + Reset
		}
		return edge("│") + gutterStyle + " " + Reset + text + Reset + strings.Repeat(" ", max(0, inner-ansi.StringWidth(text))) + " " + closing
	}
	lines = append(lines, row(f.Page.Detail, false, ""))
	body := height - DialogChrome
	if f.Tabs != "" {
		lines = append(lines, row(f.Tabs, false, ""))
		body--
	}
	lines = append(lines, edge("├"+strings.Repeat("─", width-2)+"┤"))
	thumbFrom, thumbTo := 0, -1
	if f.Total > body && body > 0 {
		size := max(1, body*body/f.Total)
		thumbFrom = (body - size) * f.Top / max(1, f.Total-body)
		thumbTo = thumbFrom + size - 1
	}
	for i := range body {
		text := ""
		gutterStyle := ""
		if i < len(f.Rows) {
			text = f.Rows[i]
		}
		if i < len(f.GutterStyles) {
			gutterStyle = f.GutterStyles[i]
		}
		lines = append(lines, row(text, i >= thumbFrom && i <= thumbTo, gutterStyle))
	}
	footer := ansi.Truncate(f.Footer, max(0, width-6), "…")
	lines = append(lines, edge("╰─ ")+footer+Reset+" "+edge(strings.Repeat("─", max(0, width-5-ansi.StringWidth(footer)))+"╯"))
	background, ink := p.CodeBackground(), "\x1b[38;2;230;237;243m"
	if background == "" {
		background = "\x1b[48;2;32;35;40m"
	}
	if p.Theme == diffrender.LightTheme {
		ink = "\x1b[38;2;31;41;55m"
	}
	for i, line := range lines {
		lines[i] = dialogSurface(line, ink, background)
	}
	return lines
}

// Restore the surface after any SGR color reset, including combined resets,
// without overwriting explicit syntax colors, diff fills, or selection styles.
func dialogSurface(row, ink, fill string) string {
	var out strings.Builder
	out.WriteString(Reset + ink + fill)
	var style uv.Style
	parser := ansi.GetParser()
	defer func() { parser.SetHandler(ansi.Handler{}); ansi.PutParser(parser) }()
	parser.SetHandler(ansi.Handler{HandleCsi: func(cmd ansi.Cmd, params ansi.Params) {
		if cmd == 'm' {
			uv.ReadStyle(params, &style)
		}
	}})
	var state byte
	for row != "" {
		seq, _, n, next := ansi.DecodeSequence(row, state, nil)
		out.WriteString(seq)
		if strings.HasPrefix(seq, "\x1b[") && strings.HasSuffix(seq, "m") {
			parser.Parse([]byte(seq))
			if style.Fg == nil {
				out.WriteString(ink)
			}
			if style.Bg == nil {
				out.WriteString(fill)
			}
		}
		row, state = row[n:], next
	}
	out.WriteString(Reset)
	return out.String()
}
