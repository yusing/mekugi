package terminalsession

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/terminalui"
)

// DialogTab describes supplied content without owning its source or effects.
type DialogTab struct {
	ID               string
	Name, Path, Code string
	Exact            bool
	Live             bool
	Line             int
}

type dialogTabHit struct{ page, from, to int }

type dialogPageState struct {
	top, match             int
	follow, typing, missed bool
	draft, query           string
}

// Dialog owns terminal navigation and frozen selection over supplied documents.
// Source: apps/mekugi/internal/router/output_dialog.go:255:312,326:362,511:833@563f0c37.
type Dialog struct {
	pages        []DialogTab
	Page, top    int
	follow       bool
	rows         int
	rect         Rect
	body         []string
	indents      []int
	tabs         []dialogTabHit
	Typing       bool
	draft, query string
	match        int
	missed       bool
	Document     terminalui.DialogPage
	Selection    *Selection
	Navigation   string
	ForceTabs    bool
	OutputToggle string
	// FileLinks is detached local-path admission supplied by the application.
	FileLinks       map[string]bool
	FileLinkDemands []string

	states      map[string]dialogPageState
	active      string
	ready       bool
	width       int
	starts      []int
	theme       diffrender.Theme
	pendingLine int
	link        string
}

type DialogAction uint8

const (
	DialogClose DialogAction = iota + 1
	DialogCopy
	DialogReference
	DialogOpenLink
)

type DialogIntent struct {
	Action DialogAction
	Text   string
}

func (d *Dialog) tabbed() bool { return len(d.pages) > 1 || d.ForceTabs }
func (d *Dialog) Chrome() int {
	if d.tabbed() {
		return terminalui.DialogChrome + 1
	}
	return terminalui.DialogChrome
}

// SetPages keeps active content selected by identity through membership changes.
func (d *Dialog) SetPages(pages []DialogTab) {
	d.pages = pages
	if len(pages) == 0 {
		return
	}
	page := min(d.Page, len(pages)-1)
	for i, p := range pages {
		if p.ID == d.active {
			page = i
			break
		}
	}
	d.ShowPage(page)
}

func (d *Dialog) ShowPage(page int) {
	if page < 0 || page >= len(d.pages) {
		return
	}
	if d.states == nil {
		d.states = make(map[string]dialogPageState)
	}
	if d.ready {
		d.states[d.active] = dialogPageState{d.top, d.match, d.follow, d.Typing, d.missed, d.draft, d.query}
	}
	d.Page, d.active, d.ready = page, d.pages[page].ID, true
	state, exists := d.states[d.active]
	if !exists {
		state.match = -1
		state.follow = d.pages[page].Live
		d.pendingLine = d.pages[page].Line
	}
	d.top, d.match, d.follow, d.Typing, d.missed, d.draft, d.query = state.top, state.match, state.follow, state.typing, state.missed, state.draft, state.query
}

func dialogSize(width, height int) (int, int) {
	if width >= 60 {
		return int(float64(width) * .9), int(float64(height) * .9)
	}
	return width, height
}

// Prepare returns the content width and dismisses selections invalidated by resize.
func (d *Dialog) Prepare(width, height int) int {
	w, h := dialogSize(width, height)
	if w < 12 || h < d.Chrome()+1 {
		d.rect = Rect{}
		d.Selection = nil
		return 0
	}
	if d.Selection != nil && (d.rect.W != w || d.rect.H > h) {
		d.Selection = nil
	}
	return w - 4
}

func (d *Dialog) SetDocument(page terminalui.DialogPage, width int) {
	d.Document, d.width = page, width
	d.starts = make([]int, len(page.Lines)+1)
	for i := range page.Lines {
		d.starts[i+1] = d.starts[i] + page.RowCount(i, width)
	}
}

// RevealLine requests a source-line jump after the next layout.
func (d *Dialog) RevealLine(line int) { d.pendingLine = line }

func (d *Dialog) navigate(first int) {
	if first >= 0 && first < len(d.Document.Lines) {
		d.top, d.follow = min(d.starts[first], d.bottom()), false
	}
}

func (d *Dialog) tabRow(width int) string {
	d.tabs = nil
	if !d.tabbed() || len(d.pages) == 0 {
		return ""
	}
	count := min(len(d.pages), max(1, width/24))
	first := max(0, min(d.Page-count/2, len(d.pages)-count))
	cell := max(1, width/count)
	var out strings.Builder
	for i := first; i < first+count; i++ {
		page := d.pages[i]
		label := fmt.Sprintf(" %d %s", i+1, page.Name)
		if page.Path != "" {
			label += " " + terminalui.StyledPath(terminalui.TruncatePath(page.Path, cell-ansi.StringWidth(label)-2))
		} else if page.Code != "" {
			label += " " + strings.SplitN(page.Code, "\n", 2)[0]
		}
		label += " "
		if page.Exact {
			label = " " + page.Name + " "
		}
		label = ansi.Truncate(label, cell, "…")
		label += strings.Repeat(" ", max(0, cell-ansi.StringWidth(label)))
		if i == d.Page {
			label = d.theme.Accent() + "\x1b[7m" + label + terminalui.Reset
		} else {
			label = terminalui.Dim + label + terminalui.Undim
		}
		d.tabs = append(d.tabs, dialogTabHit{i, (i - first) * cell, (i - first + 1) * cell})
		out.WriteString(label)
	}
	return out.String()
}

func (d *Dialog) total() int {
	if len(d.starts) == 0 {
		return 0
	}
	return d.starts[len(d.starts)-1]
}

func (d *Dialog) bottom() int { return max(0, d.total()-d.rows) }

func (d *Dialog) scroll(rows int) {
	d.top = max(0, min(d.top+rows, d.bottom()))
	d.follow = d.Document.Live && d.top >= d.bottom()
}

func (d *Dialog) find(step int) {
	if d.query == "" || len(d.Document.Lines) == 0 {
		return
	}
	n := len(d.Document.Lines)
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
		if row, found := d.Document.MatchRow(i, d.width, d.query); found {
			d.match, d.missed = i, false
			d.navigate(i)
			d.top = max(0, min(d.starts[i]+row-d.rows/3, d.bottom()))
			return
		}
	}
	d.match, d.missed = -1, true
}

func (d *Dialog) highlight(row string, indent int, current bool) string {
	if d.query == "" {
		return row
	}
	text := ansi.Cut(row, indent, ansi.StringWidth(row))
	plain := strings.ToLower(ansi.Strip(text))
	mark, unmark := "\x1b[4m", "\x1b[24m"
	var out strings.Builder
	out.WriteString(ansi.Cut(row, 0, indent))
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
			match = terminalui.TextSelection(match, d.theme)
		} else {
			match = mark + strings.ReplaceAll(match, terminalui.Reset, terminalui.Reset+mark) + unmark
		}
		out.WriteString(ansi.Cut(text, cell, from) + match)
		cell, offset = to, offset+i+len(d.query)
	}
	if cell == 0 {
		return row
	}
	return out.String() + ansi.Cut(text, cell, ansi.StringWidth(text))
}

func (d *Dialog) footer(width int) string {
	dim, undim := terminalui.Dim, terminalui.Undim
	if d.Typing {
		return "/" + d.draft + "▏" + dim + "  enter find · esc cancel" + undim
	}
	keys := []string{"↑↓ PgUp/PgDn g/G scroll"}
	navigation := "←→ " + d.Navigation
	if len(d.pages) > 1 {
		keys = append(keys, navigation)
	}
	keys = append(keys, "/ find")
	if d.query != "" {
		keys[len(keys)-1] = "/ n N find"
	}
	if d.OutputToggle != "" {
		keys = append(keys, "o "+d.OutputToggle)
	}
	keys = append(keys, "y copy", "esc")
	if d.Document.Live && !d.follow {
		keys = append([]string{"End follows"}, keys...)
	}
	footer := dim + strings.Join(keys, " · ") + undim
	if d.missed {
		footer = terminalui.Amber + "no match" + terminalui.Reset + dim + " · " + undim + footer
	}
	if ansi.StringWidth(footer) > width-6 {
		footer = dim + "↑↓ scroll · / find · y copy · esc" + undim
		if len(d.pages) > 1 {
			footer = dim + navigation + " · ↑↓ scroll · y copy · esc" + undim
		}
		if d.OutputToggle != "" {
			footer = dim + "↑↓ scroll · o " + d.OutputToggle + " · y copy · esc" + undim
		}
	}
	return footer
}

// Key consumes dialog input and returns effects for the application to deliver.
func (d *Dialog) Key(key string) DialogIntent {
	if d.Selection != nil {
		if intent, handled := d.Selection.Key(key, true); handled {
			if intent == SelectionScroll {
				d.top, d.follow = d.Selection.Top(), false
				return DialogIntent{}
			}
			return d.selectionAction(intent)
		}
	}
	d.Selection = nil
	if d.Typing {
		switch key {
		case "\x1b", "\x03":
			d.Typing = false
		case "\r":
			d.Typing, d.query, d.match = false, strings.ToLower(d.draft), -1
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
		return DialogIntent{}
	}
	switch key {
	case "\x1b", "q", "\x03":
		return DialogIntent{Action: DialogClose}
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
		d.top, d.follow = d.bottom(), d.Document.Live
	case "\x1b[D", "h":
		d.ShowPage(d.Page - 1)
	case "\x1b[C", "l":
		d.ShowPage(d.Page + 1)
	case "/":
		d.Typing, d.draft = true, ""
	case "n":
		d.find(1)
	case "N":
		d.find(-1)
	case "y":
		if d.Document.Text != "" {
			return DialogIntent{Action: DialogCopy, Text: d.Document.Text}
		}
	}
	return DialogIntent{}
}

func (d *Dialog) selectionAction(action SelectionIntent) DialogIntent {
	s := d.Selection
	if s == nil || s.Dragging() && action != SelectionClear {
		return DialogIntent{}
	}
	var intent DialogIntent
	switch action {
	case SelectionReference:
		intent = DialogIntent{Action: DialogReference, Text: s.Text()}
	case SelectionCopy:
		intent = DialogIntent{Action: DialogCopy, Text: s.Text()}
	case SelectionClear:
	default:
		return intent
	}
	d.Selection = nil
	return intent
}

// Mouse keeps pointer routing above the underlying panes, including wheel input.
func (d *Dialog) Mouse(button, x, y int, release bool) DialogIntent {
	if intent, handled := d.selectionMouse(button, x, y, release); handled {
		return intent
	}
	if release {
		return DialogIntent{}
	}
	switch button &^ 28 {
	case 64:
		d.scroll(-3)
	case 65:
		d.scroll(3)
	case 0:
		if d.tabbed() && y == d.rect.Y+2 {
			for _, tab := range d.tabs {
				if x-d.rect.X-2 >= tab.from && x-d.rect.X-2 < tab.to {
					d.ShowPage(tab.page)
					return DialogIntent{}
				}
			}
		}
		if !d.rect.Contains(x, y) || y == d.rect.Y && x >= d.rect.X+d.rect.W-terminalui.DialogCloseWidth-3 && x < d.rect.X+d.rect.W-3 {
			return DialogIntent{Action: DialogClose}
		}
	}
	return DialogIntent{}
}

// Source: apps/mekugi/internal/router/terminal_selection.go:77:185@563f0c37.
func (d *Dialog) selectionMouse(button, x, y int, release bool) (DialogIntent, bool) {
	body := Rect{d.rect.X + 2, d.rect.Y + d.Chrome() - 1, d.rect.W - 4, d.rows}
	mouseX, mouseY := x-body.X, y-body.Y
	if s := d.Selection; s != nil {
		if s.Mouse(button, mouseX, mouseY, release) {
			d.top = s.Top()
			if s.Moved() {
				d.follow = false
			}
			if release && !s.Moved() {
				d.Selection = nil
				if d.link != "" {
					return DialogIntent{Action: DialogOpenLink, Text: d.link}, true
				}
			}
			return DialogIntent{}, true
		}
		footer := Rect{d.rect.X, d.rect.Y + d.rect.H - 1, d.rect.W, 1}
		if !s.Dragging() && button == 0 && !release && footer.Contains(x, y) {
			capacity := d.rect.W - 6
			if ansi.StringWidth(SelectionHints.Render()) > capacity {
				capacity--
			}
			return d.selectionAction(SelectionIntent(SelectionHints.ActionAt(x-d.rect.X-3, capacity))), true
		}
		pressed := button &^ 28
		if !release && (pressed == 0 || pressed == 64 || pressed == 65) {
			d.Selection = nil
		}
	}
	if button&^28 != 0 || release || !body.Contains(x, y) || len(d.body) == 0 {
		return DialogIntent{}, false
	}
	s := NewSelection(Surface{Rect: Rect{0, 0, body.W, len(d.body)}, Rows: d.body, ContentLeft: d.indents}, mouseX, mouseY)
	_, d.link = Screen(body.W, body.H, d.body, mouseX, mouseY)
	var lines []string
	var indents []int
	for i := range d.Document.Lines {
		for _, row := range d.Document.Rows(i, body.W) {
			lines = append(lines, d.highlight(row, d.Document.Indent(i), i == d.match))
			indents = append(indents, d.Document.Indent(i))
		}
	}
	s.Document(lines, d.top, 0)
	if s.ScreenRows() != 0 {
		s.ContentLeft = indents
	} else {
		s.Rows = make([]string, len(d.body))
		s.CopySource = make([][]terminalui.CopySpan, len(d.body))
		for i, row := range d.body {
			s.Rows[i] = row
			if i < len(d.indents) && d.indents[i] > 0 {
				s.Rows[i], s.CopySource[i] = terminalui.ExtractCopy(row)
			}
		}
	}
	d.Selection = s
	return DialogIntent{}, true
}

// layoutBody keeps input selection and painting on the same selected viewport.
func (d *Dialog) layoutBody() []string {
	if d.pendingLine > 0 {
		for i, line := range d.Document.Lines {
			if line.Number == d.pendingLine {
				d.navigate(i)
				break
			}
		}
		d.pendingLine = 0
	}
	if d.follow {
		d.top = d.bottom()
	}
	d.top = max(0, min(d.top, d.bottom()))
	var body []string
	var indents []int
	var gutterStyles []string
	for i := sort.SearchInts(d.starts[1:], d.top+1); i < len(d.Document.Lines) && len(body) < d.rows; i++ {
		lines := d.Document.Rows(i, d.width)
		if skip := d.top - d.starts[i]; skip > 0 {
			lines = lines[min(skip, len(lines)):]
		}
		for _, line := range lines {
			body = append(body, d.highlight(line, d.Document.Indent(i), i == d.match))
			indents = append(indents, d.Document.Indent(i))
			gutterStyles = append(gutterStyles, d.Document.Lines[i].GutterStyle)
		}
	}
	body = body[:min(len(body), d.rows)]
	indents = indents[:len(body)]
	for len(body) < d.rows {
		body = append(body, "")
		indents = append(indents, 0)
	}
	d.resolveFileLinks(body)
	d.body, d.indents = body, indents
	return gutterStyles
}

func (d *Dialog) resolveFileLinks(rows []string) {
	d.FileLinkDemands = nil
	for i, row := range rows {
		var demands []string
		rows[i], demands = terminalui.ResolveFileLinks(row, d.FileLinks, d.theme.Accent())
		d.FileLinkDemands = append(d.FileLinkDemands, demands...)
	}
}

// Paint overlays the prepared document and its frozen selection on supplied rows.
func (d *Dialog) Paint(rows []string, width, height int, painter *terminalui.Painter) {
	w, h := dialogSize(width, height)
	if w < 12 || h < d.Chrome()+1 {
		d.rect, d.Selection = Rect{}, nil
		return
	}
	d.theme = painter.Theme
	if width >= 60 {
		h = min(h, max(d.Chrome()+3, d.Chrome()+d.total()))
	}
	rect := Rect{(width - w) / 2, (height - h) / 2, w, h}
	if d.Selection != nil && d.rect != rect {
		d.Selection = nil
	}
	d.rows = h - d.Chrome()
	gutterStyles := d.layoutBody()
	body := d.body
	if selected := d.Selection; selected != nil {
		for i := range body {
			body[i] = selected.Row(selected.DocumentY(i), painter.Theme)
		}
		d.resolveFileLinks(body)
	}
	frame := terminalui.DialogFrame{Tabs: d.tabRow(w - 4), Page: d.Document, Paused: d.Document.Live && !d.follow, Rows: body, GutterStyles: gutterStyles, Top: d.top, Total: d.total(), Footer: d.footer(w)}
	if d.Selection != nil {
		frame.Footer = SelectionHints.Render()
	}
	if len(d.pages) > 1 {
		frame.Position = fmt.Sprintf("%d / %d", d.Page+1, len(d.pages))
	}
	d.rect = rect
	for i := range rows {
		rows[i] = terminalui.Backdrop(rows[i])
	}
	for i, line := range painter.Dialog(frame, w, h) {
		if rect.Y+i < len(rows) {
			rows[rect.Y+i] += fmt.Sprintf("\x1b[%dG\x1b[0m%s\x1b[0m", rect.X+1, line)
		}
	}
}
