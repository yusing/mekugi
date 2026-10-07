package router

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

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

	typing              bool   // The footer reads a search query.
	draft               string // Query being typed.
	query               string // Confirmed query, lowercased.
	match               int    // Line of the current match, or -1.
	missed              bool   // The confirmed query matched nothing.
	filePath            string // Copyable path for a Markdown file link.
	fileText            string // Original file bytes for explicit whole-source copying.
	pendingLine         int    // Source line to reveal after the first layout.
	fileFirst, fileLast int    // Included Markdown link source range, inclusive.

	// Layout of the last frame, rebuilt when its page, width, theme or output changes.
	laid    activityui.DialogPage
	laidKey outputDialogKey
	starts  []int // Each line's first body row, then the total row count.

	// Segmented documents share one scroll surface, not per-item tabs.
	segments           []activityui.Block
	segmentLines       [][2]int
	pendingSegment     int // Segment index plus one, resolved after layout.
	pendingFlash       bool
	flashFrom, flashTo int // Logical line range, exclusive end.
	flashUntil         time.Time
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
	body                 string
	approval             string
	live                 bool
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
	for _, entry := range view.entries {
		if entry.Seq == snippet.run && entry.journalCard != nil {
			return u.openEntry(view, entry.Seq)
		}
	}
	block, ok := view.snippetBlock(snippet)
	if !ok || block.EditSource != "" || len(block.Questions) > 0 {
		return false
	}
	pages := []activityui.Block{block}
	if len(block.Members) > 0 {
		pages = nil
		seen := make(map[uint64]bool)
		for _, member := range block.Members {
			if member.EditSource != "" {
				if seen[member.Source] {
					continue
				}
				if edits, _ := u.activityEditPages(view, member.Source, ""); len(edits) > 0 {
					pages = append(pages, edits...)
					seen[member.Source] = true
					continue
				}
			}
			pages = append(pages, member)
		}
	}
	u.openBlocks(view, pages)
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

// openBlocks is the shared modal entry point. It never changes pane navigation.
func (u *terminalUI) openBlocks(view *liveActivityView, pages []activityui.Block) {
	if len(pages) == 0 {
		return
	}
	u.selection = nil
	u.output = &outputDialog{view: view, origins: pages, pages: pages, match: -1}
	u.output.refreshPages()
	u.output.showPage(0)
}

func (u *terminalUI) openEntry(view *liveActivityView, seq uint64) bool {
	for index, entry := range view.entries {
		if entry.Seq != seq {
			continue
		}
		blocks := slices.Clone(view.entries[index].blocks)
		if entry.journalCard != nil {
			blocks = []activityui.Block{view.journalCardBlock(entry.activityPaneEntry)}
		}
		for i := range blocks {
			blocks[i].Source = entry.Seq
			if blocks[i].Verb == "" {
				blocks[i].Verb = entry.Agent
			}
		}
		u.openBlocks(view, blocks)
		return len(blocks) > 0
	}
	return false
}

func (u *terminalUI) openAgent(agent string) {
	var pages []activityui.Block
	for _, entry := range u.agents.entries {
		if entry.Agent == agent {
			blocks := parseLiveActivity(entry.activityPaneEntry)
			for i := range blocks {
				blocks[i].Source = entry.Seq
			}
			pages = append(pages, blocks...)
		}
	}
	u.openBlocks(u.agents, pages)
}

// commandOutputPages uses only observed segment boundaries, never output text.
func (v *liveActivityView) commandOutputPages(source uint64) []activityui.Block {
	if source == 0 {
		return nil
	}
	for _, entry := range v.entries {
		if entry.Seq != source || entry.native == nil || entry.native.command == "" && len(entry.native.segments) == 0 {
			continue
		}
		// PTY, restored and lossy reports have no trustworthy output split.
		separate := len(entry.native.segments) == 1 || len(entry.native.segments) > 1 && len(entry.outputTail) == 0
		for _, segment := range entry.native.segments {
			if len(entry.native.segments) > 1 && !segment.skipped && segment.output == nil {
				separate = false
			}
		}
		if !separate {
			operations := toolOperationBlocks(entry.Text)
			if len(entry.native.segments) < 2 && len(operations) < 2 && (len(operations) == 0 || len(operations[0].Reads) < 2) {
				return nil
			}
			pages := []activityui.Block{{Source: source, Kind: "op", Verb: "Run", Label: "combined output", BatchExit: true, Code: toolActivityShellSource(appServerDisplayCommand(entry.native.command)), Lang: "bash", Output: entry.native.output, Tail: entry.outputTail, TailOmitted: entry.outputOmit, Running: entry.native.running, Body: "Per-command output boundaries were not retained for this invocation."}}
			setCommandTiming(pages, entry.activityPaneEntry)
			pages[0].Approval = entry.native.approval
			return pages
		}
		var pages []activityui.Block
		for _, block := range commandSegmentBlocks(entry.activityPaneEntry) {
			if block.Segment {
				block.Source = source
				pages = append(pages, block)
			}
		}
		setCommandTiming(pages, entry.activityPaneEntry)
		if len(pages) > 0 {
			pages[0].Approval = entry.native.approval
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
		label := fmt.Sprintf(" %d %s", i+1, block.Verb)
		path := block.Path
		if path == "" && len(block.Reads) == 1 {
			path = block.Reads[0].Path
		}
		if path != "" {
			label += " " + activityui.Path(activityui.TruncatePath(path, cell-ansi.StringWidth(label)-2))
		} else if block.Code != "" {
			label += " " + strings.SplitN(block.Code, "\n", 2)[0]
		}
		label += " "
		if d.filePath != "" {
			label = " Markdown "
			if i == 1 {
				label = " File "
			}
		}
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
		if snippet.block == editNavigationSnippet {
			u.openActivityEdit(view, snippet.run, snippet.path)
		} else if !u.openOutput(view, snippet) {
			u.openEntry(view, snippet.run)
		}
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
		state.follow = block.Live || block.Running || block.Output != nil && !block.Output.View().Done
		if d.filePath != "" && page == 1 {
			d.pendingLine = d.fileFirst
		}
	}
	d.top, d.match, d.follow, d.typing, d.missed, d.draft, d.query = state.top, state.match, state.follow, state.typing, state.missed, state.draft, state.query
}

func (d *outputDialog) refreshPages() {
	if len(d.origins) == 0 {
		return
	}
	var pages []activityui.Block
	seen := make(map[uint64]bool)
	for originIndex, origin := range d.origins {
		if origin.Source != 0 && seen[origin.Source] {
			continue
		}
		commands := d.view.commandOutputPages(origin.Source)
		if len(commands) == 0 {
			if origin.Source != 0 {
				for index, entry := range d.view.entries {
					if entry.Seq != origin.Source {
						continue
					}
					if entry.native != nil {
						origin.Approval = entry.native.approval
					}
					if origin.Verb == "Run" && entry.native != nil {
						timed := []activityui.Block{origin}
						setCommandTiming(timed, entry.activityPaneEntry)
						origin = timed[0]
						origin.Running = entry.native.running
					} else if origin.Kind != "op" && origin.Kind != "reads" {
						if entry.journalCard != nil {
							origin = d.view.journalCardBlock(entry.activityPaneEntry)
						} else if origin.Kind != "summary" && len(d.view.entries[index].blocks) == 1 || origin.Kind == "summary" && origin.Section < len(d.view.entries[index].blocks) {
							section := 0
							if origin.Kind == "summary" {
								section = origin.Section
							}
							current := d.view.entries[index].blocks[section]
							current.Source = origin.Source
							if current.Verb == "" {
								current.Verb = origin.Verb
							}
							origin = current
						}
						if entry.native != nil && entry.native.live {
							origin.Live = entry.native.phase == "summary" || entry.native.phase == "item/started" || entry.native.phase == "item/agentMessage/delta"
						}
					}
					break
				}
			}
			d.origins[originIndex] = origin // Keep the last readable snapshot if the source ages out.
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
	key := outputDialogKey{page: d.page, width: width, output: block.Output, theme: d.view.painter.Theme, body: block.Body, approval: block.Approval, live: block.Live}
	if block.Output != nil {
		key.version = block.Output.Version()
	}
	if key == d.laidKey && d.starts != nil {
		d.laid.Title = d.view.painter.DialogPageTitle(block, time.Now(), width)
		return
	}
	d.laid, d.laidKey = d.view.painter.DialogPage(block, width), key
	if d.filePath != "" {
		d.laid.Lines = append([]activityui.DialogLine{{Text: activityui.Path(livediff.Safe(d.filePath, false)), Wrap: true}, {}}, d.laid.Lines...)
		if block.Kind != "error" {
			d.laid.Text = d.fileText
			for i := range d.laid.Lines {
				line := &d.laid.Lines[i]
				if line.Number > 0 && line.Number >= d.fileFirst && line.Number <= d.fileLast {
					line.GutterStyle = d.view.painter.Theme.Accent() + d.view.painter.Theme.SelectionBackground()
				}
			}
		}
	}
	if len(d.segments) > 0 {
		flashed := slices.IndexFunc(d.segmentLines, func(lines [2]int) bool {
			return lines[0] == d.flashFrom && lines[1] == d.flashTo
		})
		d.laid.Lines, d.segmentLines = nil, nil
		var texts []string
		for _, segment := range d.segments {
			if len(d.laid.Lines) > 0 {
				d.laid.Lines = append(d.laid.Lines, activityui.DialogLine{})
			}
			first := len(d.laid.Lines)
			page := d.view.painter.DialogPage(segment, width)
			heading := activityui.Dim + "─ " + segment.Label + " "
			heading += strings.Repeat("─", max(0, width-ansi.StringWidth(heading))) + activityui.Undim
			d.laid.Lines = append(d.laid.Lines, activityui.DialogLine{Text: heading})
			d.laid.Lines = append(d.laid.Lines, page.Lines...)
			d.segmentLines = append(d.segmentLines, [2]int{first, len(d.laid.Lines)})
			texts = append(texts, segment.Label+"\n"+page.Text)
		}
		d.laid.Text = strings.Join(texts, "\n\n")
		if flashed >= 0 {
			d.flashFrom, d.flashTo = d.segmentLines[flashed][0], d.segmentLines[flashed][1]
		}
	}
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

// navigate is the shared row-target navigation for links and search results.
// Flash is opt-in and starts only once the destination has been laid out.
func (d *outputDialog) navigate(first, last int, flash bool) {
	if first < 0 || first >= len(d.laid.Lines) {
		return
	}
	d.top, d.follow = min(d.starts[first], d.bottom()), false
	d.flashFrom, d.flashTo, d.flashUntil = 0, 0, time.Time{}
	if flash {
		d.flashFrom, d.flashTo = first, min(last, len(d.laid.Lines))
		d.flashUntil = time.Now().Add(700 * time.Millisecond)
	}
}

func (d *outputDialog) expireFlash(now time.Time) bool {
	if d.flashUntil.IsZero() || now.Before(d.flashUntil) {
		return false
	}
	d.flashFrom, d.flashTo, d.flashUntil = 0, 0, time.Time{}
	return true
}

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
		if row, found := d.laid.MatchRow(i, d.laidKey.width, d.query); found {
			d.match, d.missed = i, false
			d.navigate(i, i+1, false)
			d.top = max(0, min(d.starts[i]+row-d.rows/3, d.bottom()))
			return
		}
	}
	d.match, d.missed = -1, true
}

// outputKey handles a key while the dialog is open; every key belongs to it.
func (u *terminalUI) outputKey(key string) {
	d := u.output
	if u.selectionKey(key) {
		return
	}
	// Navigation clears the snapshot before moving the page.
	u.selection = nil
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
	if u.selectionMouse(button, x, y, release) {
		return
	}
	if release {
		return
	}
	switch button &^ 28 {
	case 64:
		d.scroll(-outputDialogWheelRows)
	case 65:
		d.scroll(outputDialogWheelRows)
	case 0:
		if len(d.pages) > 1 && y == d.rect.y+2 {
			for _, tab := range d.tabs {
				if x-d.rect.x-2 >= tab.from && x-d.rect.x-2 < tab.to {
					d.showPage(tab.page)
					return
				}
			}
		}
		if !d.rect.contains(x, y) || y == d.rect.y && x >= d.rect.x+d.rect.w-activityui.DialogCloseWidth-3 && x < d.rect.x+d.rect.w-3 {
			u.output = nil
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
		d.rect, u.selection = terminalRect{}, nil
		return
	}
	if u.selection != nil && (d.rect.w != w || d.rect.h > h) {
		u.selection = nil
	}
	if u.selection == nil {
		d.layout(w - 4)
	}
	if width >= outputDialogFullWidth {
		h = min(h, max(d.chrome()+3, d.chrome()+d.total()))
	}
	if u.selection != nil && d.rect != (terminalRect{(width - w) / 2, (height - h) / 2, w, h}) {
		u.selection = nil
	}
	d.rows = h - d.chrome()
	if d.pendingLine > 0 {
		for i, line := range d.laid.Lines {
			if line.Number == d.pendingLine {
				d.navigate(i, i+1, false)
				break
			}
		}
		d.pendingLine = 0
	}
	if d.pendingSegment > 0 && d.pendingSegment <= len(d.segmentLines) {
		target := d.segmentLines[d.pendingSegment-1]
		d.navigate(target[0], target[1], d.pendingFlash)
		d.pendingSegment = 0
	}
	if d.follow {
		d.top = d.bottom()
	}
	d.top = max(0, min(d.top, d.bottom()))
	var body []string
	var indents []int
	var gutterStyles []string
	for i := sort.SearchInts(d.starts[1:], d.top+1); i < len(d.laid.Lines) && len(body) < d.rows; i++ {
		lines := d.laid.Rows(i, w-4)
		if skip := d.top - d.starts[i]; skip > 0 {
			lines = lines[min(skip, len(lines)):]
		}
		for _, line := range lines {
			line = d.highlight(line, d.laid.Indent(i), i == d.match)
			if i >= d.flashFrom && i < d.flashTo && time.Now().Before(d.flashUntil) {
				fill := d.view.painter.Theme.SelectionBackground()
				line = fill + strings.ReplaceAll(line, activityui.Reset, activityui.Reset+fill) + "\x1b[49m"
			}
			body = append(body, line)
			indents = append(indents, d.laid.Indent(i))
			gutterStyles = append(gutterStyles, d.laid.Lines[i].GutterStyle)
		}
	}
	body = body[:min(len(body), d.rows)]
	indents = indents[:len(body)]
	for len(body) < d.rows {
		body = append(body, "")
		indents = append(indents, 0)
	}
	d.body, d.indents = body, indents
	if selected := u.selection; selected != nil {
		body = slices.Clone(body)
		for i := range body {
			body[i] = selected.row(selected.documentY(i), d.view.painter.Theme)
		}
	}
	frame := activityui.DialogFrame{Tabs: d.tabRow(w - 4), Page: d.laid, Paused: d.laid.Live && !d.follow, Rows: body, GutterStyles: gutterStyles, Top: d.top, Total: d.total(), Footer: d.footer(w)}
	if u.selection != nil {
		frame.Footer = selectionHints.render()
	}
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
	out := ansi.Cut(row, 0, indent)
	cell := 0 // Cells of text copied so far.
	for offset := 0; ; {
		i := strings.Index(plain[offset:], d.query)
		if i < 0 {
			break
		}
		from := ansi.StringWidth(plain[:offset+i])
		to := from + ansi.StringWidth(d.query)
		match := ansi.Cut(text, from, to)
		if current {
			match = activityui.TextSelection(match, d.view.painter.Theme)
		} else {
			match = mark + strings.ReplaceAll(match, activityui.Reset, activityui.Reset+mark) + unmark
		}
		out += ansi.Cut(text, cell, from) + match
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
	if d.typing {
		return "/" + d.draft + "▏" + dim + "  enter find · esc cancel" + undim
	}
	keys := []string{"↑↓ PgUp/PgDn g/G scroll"}
	navigation := "←→ command"
	if d.filePath != "" {
		navigation = "←→ view"
	}
	if len(d.pages) > 1 {
		keys = append(keys, navigation)
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
			footer = dim + navigation + " · ↑↓ scroll · y copy · esc" + undim
		}
	}
	return footer
}
