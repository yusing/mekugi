package router

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

// outputDialog shows an operation's whole source and retained output over
// both panes: a command, a tool result, or each read a merged row stands for,
// paged with ←→. Live output follows its latest rows until the reader scrolls
// up; End follows again. It holds the blocks as they were opened; retained
// output keeps them current.
type outputDialog struct {
	view      *liveActivityView // Supplies the painter's theme and colors.
	pages     []activityui.Block
	origins   []activityui.Block // Original invocation members, including merged feed rows.
	states    map[outputPageKey]outputPageState
	active    outputPageKey
	pageReady bool
	page      int
	top       int  // First body row shown.
	follow    bool // Live output keeps its latest rows in view.
	rows      int  // Body rows in the last frame.
	rect      terminalRect
	body      []string // Last visible body, used as the drag snapshot.
	indents   []int
	tabs      []outputTab
	selection *terminalSelection

	typing bool   // The footer reads a search query.
	draft  string // Query being typed.
	query  string // Confirmed query, lowercased.
	match  int    // Line of the current match, or -1.
	missed bool   // The confirmed query matched nothing.

	// Layout of the last frame, rebuilt when its page, width, theme or output changes.
	laid    activityui.DialogPage
	laidKey outputDialogKey
	starts  []int // Each line's first body row, then the total row count.
}

type outputPageKey struct {
	output *activityui.Output
	source uint64
	index  int // Distinguishes output-less pages such as skipped commands.
}

type outputPageState struct {
	top, match             int
	follow, typing, missed bool
	draft, query           string
}

type outputTab struct {
	page, from, to int
}

type outputDialogKey struct {
	page, width, version int
	output               *activityui.Output
	theme                livediff.Theme
}

// Outside a narrow terminal, the dialog takes at most this share of each
// dimension.
const (
	outputDialogShare     = .9
	outputDialogFullWidth = 60 // Below this many columns it takes the whole screen.
	outputDialogWheelRows = 3
)

// openOutput opens the operation a snippet names in the dialog, reporting
// whether it names one.
func (u *terminalUI) openOutput(view *liveActivityView, snippet liveActivitySnippet) bool {
	block, ok := view.snippetBlock(snippet)
	if !ok || !outputBlock(block) {
		return false
	}
	pages := []activityui.Block{block}
	if len(block.Members) > 0 {
		pages = block.Members
	}
	u.selection = nil
	u.output = &outputDialog{view: view, origins: pages, pages: pages, match: -1}
	u.output.refreshPages()
	page := 0
	if len(block.Members) == 0 && block.Output != nil {
		for i, command := range u.output.pages {
			if command.Output == block.Output {
				page = i
				break
			}
		}
	}
	u.output.showPage(page)
	return true
}

// commandOutputPages uses only observed segment boundaries, never output text.
func (v *liveActivityView) commandOutputPages(source uint64) []activityui.Block {
	if source == 0 {
		return nil
	}
	for _, entry := range v.entries {
		if entry.Seq != source || entry.native == nil {
			continue
		}
		// PTY, restored and lossy reports have no trustworthy output split.
		separate := len(entry.native.segments) > 0 && len(entry.outputTail) == 0
		for _, segment := range entry.native.segments {
			if !segment.skipped && segment.output == nil {
				separate = false
			}
		}
		if !separate {
			operations := toolOperationBlocks(entry.Text)
			if len(entry.native.segments) < 2 && len(operations) < 2 && (len(operations) == 0 || len(operations[0].Reads) < 2) {
				return nil
			}
			return []activityui.Block{{Source: source, Kind: "op", Verb: "Run", Label: "combined output", BatchExit: true, Code: entry.native.command, Lang: "bash", Output: entry.native.output, Tail: entry.outputTail, TailOmitted: entry.outputOmit, Running: entry.native.running, Body: "Per-command output boundaries were not retained for this invocation."}}
		}
		var pages []activityui.Block
		for _, block := range commandSegmentBlocks(entry) {
			if block.Segment {
				block.Source = source
				pages = append(pages, block)
			}
		}
		return pages
	}
	return nil
}

func (d *outputDialog) chrome() int {
	if len(d.pages) > 1 {
		return activityui.DialogChrome + 1
	}
	return activityui.DialogChrome
}

// tabRow keeps the active command and its nearest neighbors visible.
func (d *outputDialog) tabRow(width int) string {
	d.tabs = nil
	if len(d.pages) < 2 {
		return ""
	}
	count := min(len(d.pages), max(1, width/24))
	first := max(0, min(d.page-count/2, len(d.pages)-count))
	cell := max(1, width/count)
	var out strings.Builder
	for i := first; i < first+count; i++ {
		block := d.pages[i]
		label := block.Verb
		if len(block.Reads) == 1 {
			label += " " + block.Reads[0].Path
		} else if block.Code != "" {
			label += " " + strings.SplitN(block.Code, "\n", 2)[0]
		}
		label = fmt.Sprintf(" %d %s ", i+1, label)
		label = ansi.Truncate(label, cell, "…")
		label += strings.Repeat(" ", max(0, cell-ansi.StringWidth(label)))
		if i == d.page {
			label = d.view.painter.Theme.Accent() + "\x1b[7m" + label + activityui.Reset
		} else {
			label = activityui.Dim + label + activityui.Undim
		}
		d.tabs = append(d.tabs, outputTab{i, (i - first) * cell, (i - first + 1) * cell})
		out.WriteString(label)
	}
	return out.String()
}

// openRequested opens an operation the view's pointer handling clicked.
func (u *terminalUI) openRequested(view *liveActivityView) {
	if snippet := view.opening; snippet != (liveActivitySnippet{}) {
		view.opening = liveActivitySnippet{}
		u.openOutput(view, snippet)
	}
}

func (d *outputDialog) pageKey(page int) outputPageKey {
	block := d.pages[page]
	if block.Output != nil {
		return outputPageKey{output: block.Output}
	}
	return outputPageKey{source: block.Source, index: page}
}

func (d *outputDialog) showPage(page int) {
	if d.states == nil {
		d.states = make(map[outputPageKey]outputPageState)
	}
	if d.pageReady {
		d.states[d.active] = outputPageState{d.top, d.match, d.follow, d.typing, d.missed, d.draft, d.query}
	}
	d.page, d.active, d.pageReady = page, d.pageKey(page), true
	state, exists := d.states[d.active]
	if !exists {
		block := d.pages[page]
		state.match = -1
		state.follow = block.Running || block.Output != nil && !block.Output.View().Done
	}
	d.top, d.match, d.follow, d.typing, d.missed, d.draft, d.query = state.top, state.match, state.follow, state.typing, state.missed, state.draft, state.query
}

func (d *outputDialog) refreshPages() {
	if len(d.origins) == 0 {
		return
	}
	var pages []activityui.Block
	seen := make(map[uint64]bool)
	for _, origin := range d.origins {
		if origin.Source != 0 && seen[origin.Source] {
			continue
		}
		commands := d.view.commandOutputPages(origin.Source)
		if len(commands) == 0 {
			pages = append(pages, origin)
		} else {
			seen[origin.Source] = true
			pages = append(pages, commands...)
		}
	}
	d.pages = pages
	page := min(d.page, len(pages)-1)
	for i := range pages {
		if d.pageKey(i) == d.active {
			page = i
			break
		}
	}
	if d.pageReady {
		d.showPage(page)
	}
}

// layout rebuilds the page for a body width when anything it shows changed.
func (d *outputDialog) layout(width int) {
	d.refreshPages()
	block := d.pages[d.page]
	key := outputDialogKey{page: d.page, width: width, output: block.Output, theme: d.view.painter.Theme}
	if block.Output != nil {
		key.version = block.Output.Version()
	}
	if key == d.laidKey && d.starts != nil {
		return
	}
	d.laid, d.laidKey = d.view.painter.DialogPage(block, width), key
	d.starts = make([]int, len(d.laid.Lines)+1)
	for i := range d.laid.Lines {
		d.starts[i+1] = d.starts[i] + d.laid.RowCount(i, width)
	}
}

func (d *outputDialog) total() int {
	if len(d.starts) == 0 {
		return 0
	}
	return d.starts[len(d.starts)-1]
}

func (d *outputDialog) bottom() int { return max(0, d.total()-d.rows) }

// scroll moves the body by rows; reaching the bottom of live output follows it.
func (d *outputDialog) scroll(rows int) {
	d.top = max(0, min(d.top+rows, d.bottom()))
	d.follow = d.laid.Live && d.top >= d.bottom()
}

// find moves to the next line matching the query after the current match, or
// the previous one before it, wrapping around.
func (d *outputDialog) find(step int) {
	if d.query == "" || len(d.laid.Lines) == 0 {
		return
	}
	n := len(d.laid.Lines)
	from := d.match
	if from < 0 {
		// The first search starts at the top of the view.
		from = sort.SearchInts(d.starts[:n], d.top+1) - 1
		if step < 0 {
			from++
		} else {
			from--
		}
	}
	for k := 1; k <= n; k++ {
		i := ((from+step*k)%n + n) % n
		if strings.Contains(strings.ToLower(ansi.Strip(d.laid.Lines[i].Text)), d.query) {
			d.match, d.missed = i, false
			d.top, d.follow = max(0, min(d.starts[i]-d.rows/3, d.bottom())), false
			return
		}
	}
	d.match, d.missed = -1, true
}

// outputKey handles a key while the dialog is open; every key belongs to it.
func (u *terminalUI) outputKey(key string) {
	d := u.output
	if s := d.selection; s != nil {
		switch key {
		case "\x1b":
			d.selection = nil
			return
		case "y", "c", "\x03":
			if !s.dragging {
				u.copyText(s.text())
				d.selection = nil
			}
			return
		}
		// Navigation clears the snapshot before moving the page.
		d.selection = nil
	}
	if d.typing {
		switch key {
		case "\x1b", "\x03":
			d.typing = false
		case "\r":
			d.typing, d.query, d.match = false, strings.ToLower(d.draft), -1
			d.find(1)
		case "\x7f", "\b":
			if r := []rune(d.draft); len(r) > 0 {
				d.draft = string(r[:len(r)-1])
			}
		default:
			if !strings.HasPrefix(key, "\x1b") && key >= " " {
				d.draft += key
			}
		}
		return
	}
	switch key {
	case "\x1b", "q", "\x03":
		u.output = nil
	case "\x1b[A", "k":
		d.scroll(-1)
	case "\x1b[B", "j", "\r":
		d.scroll(1)
	case "\x1b[5~", "b":
		d.scroll(-max(1, d.rows-1))
	case "\x1b[6~", " ":
		d.scroll(max(1, d.rows-1))
	case "\x1b[H", "\x1b[1~", "g":
		d.top, d.follow = 0, false
	case "\x1b[F", "\x1b[4~", "G":
		d.top, d.follow = d.bottom(), d.laid.Live
	case "\x1b[D", "h":
		if d.page > 0 {
			d.showPage(d.page - 1)
		}
	case "\x1b[C", "l":
		if d.page < len(d.pages)-1 {
			d.showPage(d.page + 1)
		}
	case "/":
		d.typing, d.draft = true, ""
	case "n":
		d.find(1)
	case "N":
		d.find(-1)
	case "y":
		if d.laid.Text != "" {
			u.copyText(d.laid.Text)
		}
	}
}

// outputMouse handles a pointer event while the dialog is open. The wheel
// scrolls the dialog wherever the pointer is, never the feed behind it; a
// press outside closes it.
func (u *terminalUI) outputMouse(button, x, y int, release bool) {
	d := u.output
	body := terminalRect{d.rect.x + 2, d.rect.y + d.chrome() - 1, d.rect.w - 4, d.rows}
	if selected := d.selection; selected != nil && selected.dragging && (release || button&32 != 0 && button&3 == 0) {
		selected.move(x-body.x, y-body.y, release)
		if selected.moved {
			d.follow = false
		}
		if release && !selected.moved {
			d.selection = nil
		}
		return
	}
	if release {
		return
	}
	switch button &^ 28 {
	case 64:
		d.selection = nil
		d.scroll(-outputDialogWheelRows)
	case 65:
		d.selection = nil
		d.scroll(outputDialogWheelRows)
	case 0:
		d.selection = nil
		if len(d.pages) > 1 && y == d.rect.y+2 {
			for _, tab := range d.tabs {
				if x-d.rect.x-2 >= tab.from && x-d.rect.x-2 < tab.to {
					d.showPage(tab.page)
					return
				}
			}
		}
		if !d.rect.contains(x, y) {
			u.output = nil
		} else if body.contains(x, y) && len(d.body) > 0 {
			d.selection = &terminalSelection{rect: terminalRect{0, 0, body.w, len(d.body)}, rows: d.body, contentLeft: d.indents, startX: x - body.x, startY: y - body.y, endX: x - body.x, endY: y - body.y, dragging: true}
		}
	}
}

// paintOutput draws the dialog over the faded screen above the status row: at
// most nine tenths of each dimension, fitted to its content, and the whole
// screen when the terminal is narrow.
func (u *terminalUI) paintOutput(rows []string, width, height int) {
	d := u.output
	w, h := width, height
	if width >= outputDialogFullWidth {
		w, h = int(float64(width)*outputDialogShare), int(float64(height)*outputDialogShare)
	}
	if w < 12 || h < d.chrome()+1 {
		d.rect, d.selection = terminalRect{}, nil
		return
	}
	if d.selection != nil && (d.rect.w != w || d.rect.h > h) {
		d.selection = nil
	}
	if d.selection == nil {
		d.layout(w - 4)
	}
	if width >= outputDialogFullWidth {
		h = min(h, max(d.chrome()+3, d.chrome()+d.total()))
	}
	if d.selection != nil && d.rect != (terminalRect{(width - w) / 2, (height - h) / 2, w, h}) {
		d.selection = nil
	}
	d.rows = h - d.chrome()
	if d.follow {
		d.top = d.bottom()
	}
	d.top = max(0, min(d.top, d.bottom()))
	var body []string
	var indents []int
	for i := sort.SearchInts(d.starts[1:], d.top+1); i < len(d.laid.Lines) && len(body) < d.rows; i++ {
		lines := d.laid.Rows(i, w-4)
		if skip := d.top - d.starts[i]; skip > 0 {
			lines = lines[min(skip, len(lines)):]
		}
		for _, line := range lines {
			body = append(body, d.highlight(line, d.laid.Indent(i), i == d.match))
			indents = append(indents, d.laid.Indent(i))
		}
	}
	body = body[:min(len(body), d.rows)]
	indents = indents[:len(body)]
	for len(body) < d.rows {
		body = append(body, "")
		indents = append(indents, 0)
	}
	d.body, d.indents = body, indents
	if selected := d.selection; selected != nil {
		body = slices.Clone(body)
		for i := range body {
			body[i] = selected.row(i)
		}
	}
	frame := activityui.DialogFrame{Tabs: d.tabRow(w - 4), Page: d.laid, Paused: d.laid.Live && !d.follow, Rows: body, Top: d.top, Total: d.total(), Footer: d.footer(w)}
	if len(d.pages) > 1 {
		frame.Position = fmt.Sprintf("%d / %d", d.page+1, len(d.pages))
	}
	x, y := (width-w)/2, (height-h)/2
	d.rect = terminalRect{x, y, w, h}
	for i := range rows {
		rows[i] = activityui.Backdrop(rows[i])
	}
	for i, line := range d.view.painter.Dialog(frame, w, h) {
		if y+i < len(rows) {
			rows[y+i] += fmt.Sprintf("\x1b[%dG\x1b[0m%s\x1b[0m", x+1, line)
		}
	}
}

// highlight marks the query's matches on a body row after its indent cells,
// the current match's line more strongly.
func (d *outputDialog) highlight(row string, indent int, current bool) string {
	if d.query == "" {
		return row
	}
	text := ansi.Cut(row, indent, ansi.StringWidth(row))
	plain := strings.ToLower(ansi.Strip(text))
	mark, unmark := "\x1b[4m", "\x1b[24m"
	if current {
		mark, unmark = "\x1b[7m", "\x1b[27m"
	}
	out := ansi.Cut(row, 0, indent)
	cell := 0 // Cells of text copied so far.
	for offset := 0; ; {
		i := strings.Index(plain[offset:], d.query)
		if i < 0 {
			break
		}
		from := ansi.StringWidth(plain[:offset+i])
		to := from + ansi.StringWidth(d.query)
		out += ansi.Cut(text, cell, from) + mark + ansi.Strip(ansi.Cut(text, from, to)) + unmark
		cell, offset = to, offset+i+len(d.query)
	}
	if cell == 0 {
		return row
	}
	return out + ansi.Cut(text, cell, ansi.StringWidth(text))
}

// footer is the dialog's controls, or the search being typed.
func (d *outputDialog) footer(width int) string {
	dim, undim := activityui.Dim, activityui.Undim
	if d.selection != nil {
		return dim + "drag select · y / ctrl+c copy · esc clear" + undim
	}
	if d.typing {
		return "/" + d.draft + "▏" + dim + "  enter find · esc cancel" + undim
	}
	keys := []string{"↑↓ PgUp/PgDn g/G scroll"}
	if len(d.pages) > 1 {
		keys = append(keys, "←→ command")
	}
	keys = append(keys, "/ find")
	if d.query != "" {
		keys[len(keys)-1] = "/ n N find"
	}
	keys = append(keys, "y copy", "esc")
	if d.laid.Live && !d.follow {
		keys = append([]string{"End follows"}, keys...)
	}
	footer := dim + strings.Join(keys, " · ") + undim
	if d.missed {
		footer = activityui.Amber + "no match" + activityui.Reset + dim + " · " + undim + footer
	}
	if ansi.StringWidth(footer) > width-6 {
		footer = dim + "↑↓ scroll · / find · y copy · esc" + undim
		if len(d.pages) > 1 {
			footer = dim + "←→ command · ↑↓ scroll · y copy · esc" + undim
		}
	}
	return footer
}
