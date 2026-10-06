package activity

import (
	"cmp"
	"context"
	"regexp"
	"strconv"
	"strings"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
)

// DialogPage is one invocation's source and output as the output dialog
// shows them. Lines stay unwrapped, so a long live stream costs a layout only
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
	Wrap   bool   // Keep an unnumbered logical line intact for search; wrap only for display.
	Number int    // Zero leaves the number column blank.
	Gutter string // "│" before source, "┆" before output, empty for other rows.
	Text   string // Styled.
}

// dialogHighlightBytes bounds the content the dialog colors; larger content
// shows plain rather than stalling a frame.
const dialogHighlightBytes = 256 << 10

// DialogPage lays out a block's whole source and retained output. Source keeps
// the code gutter and output the dashed one, as in the feed; a read's content
// is source, numbered from the first line it read. Markdown bodies wrap to
// width; numbered lines wrap as they are shown.
func (p *Painter) DialogPage(block Block, width int) DialogPage {
	width = max(8, width)
	view := OutputView{Lines: block.Tail, Dropped: block.TailOmitted, Done: !block.Running, Exited: !block.Running, Exit: block.ExitCode}
	if block.Output != nil {
		view = block.Output.View()
		if view.Released {
			view.Lines, view.Dropped = block.Tail, block.TailOmitted
		}
		block.Running = !view.Done
	}
	page := DialogPage{Title: p.DialogPageTitle(block, p.now(), width), Live: block.Live || !view.Done}
	var detail []string
	if len(block.Reads) == 1 && len(block.Reads[0].Ranges) > 0 {
		detail = append(detail, lineRanges(block.Reads[0].Ranges))
	}
	if total := view.Dropped + len(view.Lines); total > 0 {
		detail = append(detail, lineCount(total))
	}
	switch {
	case view.Exited && view.Exit != 0:
		detail = append(detail, Red+"exit "+strconv.Itoa(view.Exit)+Reset+Dim)
	case view.Exited && block.Verb == "Run":
		detail = append(detail, "exit 0")
	}
	page.Detail = Dim + strings.Join(detail, " · ") + Undim
	if len(detail) == 0 && block.Detail != "" {
		page.Detail = block.Detail
	}
	if block.Skipped {
		page.Detail = Dim + "skipped" + Undim
	}
	add := func(line DialogLine) {
		page.Lines = append(page.Lines, line)
		if line.Number > 0 {
			page.digits = max(page.digits, len(strconv.Itoa(line.Number)))
		}
	}
	if block.Approval != "" {
		add(DialogLine{Text: Dim + "Approval: " + Undim + approvalLabel(block.Approval)})
		if ansi.Strip(approvalLabel(block.Approval)) != block.Approval {
			add(DialogLine{Text: Dim + livediff.Safe(block.Approval, false) + Undim, Wrap: true})
		}
	}
	gap := func() {
		if len(page.Lines) > 0 {
			add(DialogLine{})
		}
	}

	// Keep timestamps in distinct metadata rows rather than clipping both
	// into the dialog's single-line status strip. They are not copied output.
	startedLabel, endedLabel, elapsedLabel := "Started ", "Ended   ", "Elapsed "
	if block.NotificationTiming {
		startedLabel, endedLabel, elapsedLabel = "Observed start ", "Observed end   ", "Host elapsed   "
	}
	if !block.Started.IsZero() {
		add(DialogLine{Text: Dim + startedLabel + block.Started.Local().Format("2006-01-02 15:04:05.000 MST") + Undim})
	}
	if !block.Ended.IsZero() {
		add(DialogLine{Text: Dim + endedLabel + block.Ended.Local().Format("2006-01-02 15:04:05.000 MST") + Undim})
		if block.Duration > 0 {
			add(DialogLine{Text: Dim + elapsedLabel + block.Duration.String() + Undim})
		}
	}
	if !block.Started.IsZero() {
		gap()
	}

	code := livediff.Safe(block.Code, false)
	page.Text = code
	if code != "" && (block.Path != "" || strings.Contains(code, "\n") || ansi.StringWidth(code) > width/2) {
		colored := strings.Split(code, "\n")
		if len(code) <= dialogHighlightBytes {
			colored = p.dialogHighlight(block, code)
		}
		for i, line := range colored {
			add(DialogLine{Number: i + 1, Gutter: "│", Text: line})
		}
		page.Text = code
	}
	if block.Body != "" {
		page.Text = block.Body
		if block.Kind == "error" {
			page.Text = livediff.Safe(block.Body, false)
		}
		gap()
		rows := block.Rows
		if block.Kind == "error" {
			for line := range strings.SplitSeq(page.Text, "\n") {
				add(DialogLine{Text: Red + line + Reset, Wrap: true})
			}
		} else {
			if rows == nil {
				rows = func(width int) []string { return p.markdown(block.Body, width, block.Kind == "summary") }
			}
			for _, row := range rows(width) {
				add(DialogLine{Text: row})
			}
		}
	}
	var notes []string
	switch {
	case view.Released:
		notes = append(notes, "Full output was released; its last available lines follow.")
	case view.Dropped > 0:
		notes = append(notes, Elision{Hidden: view.Dropped, Form: ElisionEarlier}.Text()+" not retained")
	}
	if len(view.Lines) == 0 {
		switch {
		case !view.Done:
			notes = append(notes, "Waiting for output…")
		case len(page.Lines) == 0:
			notes = append(notes, "No output")
		}
	}
	if len(notes) > 0 || len(view.Lines) > 0 {
		gap()
	}
	for _, note := range notes {
		for _, row := range Wrap(Dim+note+Undim, width, false) {
			add(DialogLine{Text: row})
		}
	}
	if len(view.Lines) == 0 {
		return page
	}
	content := strings.Join(view.Lines, "\n")
	page.Text = content
	if (block.Verb == "Skill" || block.Verb == "Attached skill") && block.ReadOutput() {
		for _, row := range p.Markdown(content, width) {
			add(DialogLine{Text: row})
		}
		return page
	}
	colored, first, gutter := p.outputColors(block, view.Lines), view.Dropped+1, "┆"
	if block.ReadOutput() && len(block.Reads) == 1 {
		// A read's output is the file it read.
		gutter = "│"
		if ranges := block.Reads[0].Ranges; len(ranges) == 1 {
			if from, _, ok := strings.Cut(ranges[0], ":"); ok {
				if n, err := strconv.Atoi(from); err == nil && n > 0 {
					first = n + view.Dropped
				}
			}
		}
	}
	for i, line := range colored {
		add(DialogLine{Number: first + i, Gutter: gutter, Text: line})
	}
	return page
}

// Search rows supply their own file type; never color them as the shell
// program that produced them. Keep prefixes and copied bytes intact.
var dialogSearchRow = regexp.MustCompile(`^(.+?):([0-9]+):(.*)$`)

func (p *Painter) dialogSearchLine(block Block, line string) string {
	parts := dialogSearchRow.FindStringSubmatch(line)
	if parts == nil {
		// rg omits the filename when exactly one file was requested.
		_, target, ok := strings.Cut(block.Label, " in ")
		path, end, valid := liveActivityCodeSpan(target, 0)
		number, content, numbered := strings.Cut(line, ":")
		n, err := strconv.Atoi(number)
		if !ok || !valid || end != len(target) || !numbered || err != nil || n < 1 {
			return line
		}
		if rows, err := p.syntax.ColorSource(context.Background(), p.Theme, path, content+"\n"); err == nil && len(rows) == 1 {
			content = rows[0]
		}
		return Dim + number + ":" + Undim + content
	}
	content := parts[3]
	if rows, err := p.syntax.ColorSource(context.Background(), p.Theme, parts[1], content+"\n"); err == nil && len(rows) == 1 {
		content = rows[0]
	}
	return Path(parts[1]) + Dim + ":" + parts[2] + ":" + Undim + content
}

// dialogLanguage uses the same source language for the title and body.
func dialogLanguage(block Block) string {
	if !block.Fenced && (block.Verb == "MCP" || strings.HasPrefix(block.Verb, "Tool")) {
		return "json"
	}
	if block.Lang == "" && block.Verb == "Run" {
		return "bash"
	}
	return block.Lang
}

func (p *Painter) dialogHighlight(block Block, source string) []string {
	lang := dialogLanguage(block)
	if path := cmp.Or(block.SyntaxPath, block.Path); path != "" && !p.LayoutOnly {
		var rows []string
		var err error
		if strings.EqualFold(lang, "diff") {
			rows, err = p.syntax.ColorDiffPath(context.Background(), p.Theme, path, source)
		} else {
			rows, err = p.syntax.ColorSource(context.Background(), p.Theme, path, source+"\n")
		}
		if err == nil && len(rows) > 0 {
			return rows
		}
	}
	if !strings.EqualFold(lang, "diff") {
		source += "\n" // The source renderer consumes one line terminator; diff does not.
	}
	return p.Highlight(lang, source)
}

// dialogTarget styles what the operation acted on.
func dialogTarget(p *Painter, block Block) string {
	switch {
	case block.BatchExit:
		return p.Label(block.Verb, block.Label)
	case block.Kind == "reads":
		var paths []string
		for _, read := range block.Reads {
			paths = append(paths, Path(livediff.Safe(read.Path, false)))
		}
		return strings.Join(paths, ", ")
	case block.Path != "":
		return Path(livediff.Safe(block.Path, false))
	case block.Code != "":
		first, _, _ := strings.Cut(livediff.Safe(block.Code, false), "\n")
		if len(first) <= dialogHighlightBytes {
			return strings.Join(p.dialogHighlight(block, first), " ")
		}
		return first
	}
	return strings.TrimSpace(p.Label(block.Verb, block.Label))
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
	line := d.Lines[i]
	if line.Gutter == "" {
		if line.Wrap {
			return Wrap(line.Text, max(1, width), true)
		}
		return []string{line.Text}
	}
	parts := Wrap(line.Text, d.textWidth(width), true)
	rows := make([]string, len(parts))
	for k, part := range parts {
		number := ""
		if k == 0 && line.Number > 0 {
			number = strconv.Itoa(line.Number)
		}
		rows[k] = Dim + strings.Repeat(" ", d.digits-len(number)) + number + " " + line.Gutter + Undim + " " + part
	}
	return rows
}

// MatchRow finds a case-insensitive substring in a logical line and locates
// its first display row. Search must not lose text at visual wrap boundaries.
func (d DialogPage) MatchRow(i, width int, query string) (int, bool) {
	text := strings.ToLower(ansi.Strip(d.Lines[i].Text))
	at := strings.Index(text, strings.ToLower(query))
	if at < 0 {
		return 0, false
	}
	if d.Lines[i].Wrap {
		for _, first := range text[at:] {
			return wrappedRows(text[:at]+string(first), max(1, width)) - 1, true
		}
	}
	return 0, true
}

// DialogFrame is one frame of the output dialog, before layout.
type DialogFrame struct {
	Tabs     string // Styled command selector, empty for a single result.
	Page     DialogPage
	Position string   // Page position among a merged row's invocations, such as "2 / 4".
	Paused   bool     // Live output the reader scrolled away from.
	Rows     []string // Visible body rows.
	Top      int      // Index of the first visible body row, for the scroll thumb.
	Total    int      // Body rows in all.
	Footer   string   // Styled controls.
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
	row := func(text string, thumb bool) string {
		text = ansi.Truncate(text, inner, "…")
		closing := edge("│")
		if thumb {
			closing = p.Theme.Accent() + "▌" + Reset
		}
		return edge("│") + " " + text + Reset + strings.Repeat(" ", max(0, inner-ansi.StringWidth(text))) + " " + closing
	}
	lines = append(lines, row(f.Page.Detail, false))
	body := height - DialogChrome
	if f.Tabs != "" {
		lines = append(lines, row(f.Tabs, false))
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
		if i < len(f.Rows) {
			text = f.Rows[i]
		}
		lines = append(lines, row(text, i >= thumbFrom && i <= thumbTo))
	}
	footer := ansi.Truncate(f.Footer, max(0, width-6), "…")
	lines = append(lines, edge("╰─ ")+footer+Reset+" "+edge(strings.Repeat("─", max(0, width-5-ansi.StringWidth(footer)))+"╯"))
	background, ink := p.codeBackground(), "\x1b[38;2;230;237;243m"
	if background == "" {
		background = "\x1b[48;2;32;35;40m"
	}
	if p.Theme == livediff.LightTheme {
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

func (p *Painter) DialogPageTitle(block Block, now time.Time, width int) string {
	if block.Output != nil {
		block.Running = !block.Output.View().Done
	}
	verb := RowVerb(block)
	if verb == "" {
		switch block.Kind {
		case "summary":
			verb, _ = p.thinkingHeader(block, max(1, width-7))
		case "final":
			verb = "Answer"
		case "error":
			verb = "Error"
		default:
			verb = "Message"
		}
	}
	title := VerbColor(block.Verb) + "\x1b[1m" + verb + Reset
	if target := dialogTarget(p, block); target != "" {
		title += Dim + " · " + Undim + target
	}
	if outcome := approvalLabel(block.Approval); outcome != "" {
		title += Dim + " · " + Undim + outcome
	}
	if elapsed := RunElapsed(block, now); elapsed != "" {
		title += Dim + " · " + elapsed + Undim
	}
	return title
}
