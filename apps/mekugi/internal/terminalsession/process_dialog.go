package terminalsession

import (
	"cmp"
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/pathdisplay"
	"github.com/yusing/mekugi/libraries/terminalui"
	"github.com/yusing/mekugi/libraries/textsanitize"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

func processStatus(page sessionpresentation.DetailItem) string {
	switch {
	case page.Running:
		return "running"
	case page.Exited:
		return fmt.Sprintf("exit %d", page.ExitCode)
	default:
		return "completed"
	}
}

// Divider, owner, status, runtime, command, two frame edges, output, footer.
const processDetailMinHeight = 9

type processPane struct {
	invocation                  bool
	logState                    dialogPageState
	seenLines, outputLines      int
	completion                  string
	dropped                     int
	command                     string
	commandWidth, commandBudget int
	commandTheme                diffrender.Theme
	commandRows                 []string
}

// processDetailHeight measures the same wrapped rows the viewport will paint.
func (d *ContentDialog) processDetailHeight(width, limit int) int {
	if width < 12 || limit < processDetailMinHeight {
		return limit
	}
	if d.Selection != nil && (d.rect.W != width || d.rect.H > limit) {
		d.Selection = nil
	}
	width -= 4
	if d.Selection == nil {
		d.Layout(width)
	}
	commandRows := d.processCommandRows(d.Pages[d.Page].Code, width, limit)
	fixed := processDetailMinHeight - 2 + len(commandRows)
	return fixed + min(max(1, d.total()), limit-fixed)
}

// paintProcesses keeps the selector or fixed detail chrome in the Main pane.
func (d *ContentDialog) paintProcesses(rows []string, rect Rect, limit int) {
	minimum := d.Chrome() + 1
	if d.ProcessDetail {
		minimum = processDetailMinHeight
	}
	if rect.W < 12 || rect.H < minimum {
		d.rect, d.Selection = Rect{}, nil
		return
	}
	if d.rect != rect {
		d.Selection = nil
	}
	d.rect, d.theme = rect, d.View.Painter.Theme
	width := rect.W - 4
	d.rows, d.bodyOffset, d.bodyInset = rect.H-d.Chrome(), 0, 0
	lines := []string{processHeading(d.theme.Accent(), "Background", "", width)}
	if !d.ProcessDetail {
		padding := d.rows > 1
		if padding {
			d.rows--
		}
		d.Layout(width)
		d.layoutBody()
		lines[0] = processHeading(d.theme.Accent(), d.Document.Title, d.Document.Detail, width)
		columns := "  agent · command"
		columns += strings.Repeat(" ", max(1, width-1-ansi.StringWidth(columns)-len("status"))) + "status"
		lines = append(lines, "", terminalui.Dim+columns+terminalui.Undim)
		lines = append(lines, d.body...)
		if padding {
			lines = append(lines, "")
		}
	} else {
		lines = d.appendProcessDetail(lines, width, limit)
	}
	footer := d.footer(d.View.Painter.Theme, rect.W)
	if d.Selection != nil && !d.Selection.Dragging() {
		footer = " " + SelectionHints.Render()
	} else if d.ProcessDetail && d.process.completion != "" && !d.Typing {
		notice := terminalui.Dim + d.process.completion + terminalui.Reset
		if space := width - ansi.StringWidth(footer) - ansi.StringWidth(notice); space >= 3 {
			footer += strings.Repeat(" ", space) + notice
		} else {
			footer = notice
		}
	}
	lines = append(lines, footer)
	for i, line := range lines {
		if y := rect.Y + i; y >= 0 && y < len(rows) {
			rows[y] += fmt.Sprintf("\x1b[%dG\x1b[0m  %s\x1b[0m", rect.X+1, ansi.Truncate(line, width, ""))
		}
	}
}

// processHeading matches the sheet heading: an accent rule carrying the
// title and its dim detail.
func processHeading(accent, title, detail string, width int) string {
	label := boldProcessLabel(title)
	if detail != "" {
		label += terminalui.Dim + " · " + detail + terminalui.Undim
	}
	label = ansi.Truncate(label, max(1, width-4), "…")
	return accent + "── " + terminalui.Reset + label + " " + accent + strings.Repeat("─", max(0, width-4-ansi.StringWidth(label))) + terminalui.Reset
}

func boldProcessLabel(text string) string { return "\x1b[1m" + text + "\x1b[22m" }

func processSummary(command string) string {
	return strings.Join(strings.Fields(textsanitize.Safe(command, false)), " ")
}

func (d *ContentDialog) processRuntime(page sessionpresentation.DetailItem) string {
	elapsed := activityui.RunElapsed(activityui.Block{Value: page.Value}, d.View.Now())
	if elapsed == "" {
		return "0s"
	}
	return elapsed
}

func (d *ContentDialog) updateProcesses(view sessionpresentation.DetailView) {
	if d.ProcessDetail && len(d.Pages) > 0 {
		// Pages join in start order and remain until the selection closes.
		for i, next := range view.Pages {
			if i < len(d.Pages) && i != d.Page && next.ID == d.Pages[i].ID &&
				d.Pages[i].Running && !next.Running {
				d.process.completion = processSummary(next.Code) + " · " +
					processStatus(next) + " · " + d.processRuntime(next)
			}
		}
	}
	if !d.ready && len(view.Pages) == 1 && view.Pages[0].Running {
		d.ProcessDetail, d.follow = true, true
	}
}

func (d *ContentDialog) appendProcessDetail(lines []string, width, limit int) []string {
	page := d.Pages[d.Page]
	owner := textsanitize.Safe(pathdisplay.AgentDisplayName(page.Owner), false)
	lines = append(lines,
		boldProcessLabel(ansi.Truncate(owner, width, "…")),
		boldProcessLabel("Status:")+" "+processStatus(page),
		boldProcessLabel("Runtime:")+" "+d.processRuntime(page),
	)
	lines = append(lines, d.processCommandRows(page.Code, width, limit)...)
	// The shared dialog already reserves three rows above the body.
	// The output frame's top edge adds one to these metadata rows.
	d.bodyOffset, d.bodyInset = len(lines)-2, 1
	d.rows -= d.bodyOffset + 1 // Bottom edge has its own row.
	if d.Selection == nil {
		d.Layout(width)
	}
	d.layoutBody()
	label := "Invocation"
	if !d.process.invocation {
		count := d.process.outputLines
		label = fmt.Sprintf("Output · %d lines", count)
		if count == 1 {
			label = "Output · 1 line"
		}
		if d.follow {
			d.process.seenLines = count
			if d.Document.Live {
				label += " · Following"
			}
		} else if d.Document.Live || count > d.process.seenLines {
			if unread := max(0, count-d.process.seenLines); unread > 0 {
				label += fmt.Sprintf(" · +%d new · End to follow", unread)
			} else {
				label += " · End to follow"
			}
		}
	}
	color, reset := d.theme.Accent(), terminalui.Reset
	label = ansi.Truncate(label, width-5, "…")
	// Place the frame one cell outside the selectable body, leaving its offsets
	// identical to the shared dialog's text-selection geometry.
	lines = append(lines, color+"╭─ "+reset+boldProcessLabel(label)+" "+color+
		strings.Repeat("─", width-ansi.StringWidth(label)-5)+"╮"+reset)
	for i, row := range d.body {
		if d.Selection != nil {
			row = d.Selection.Row(d.Selection.DocumentY(i), d.theme)
		}
		lines = append(lines, color+"│"+reset+row+strings.Repeat(" ", max(0, width-2-ansi.StringWidth(row)))+color+"│"+reset)
	}
	return append(lines, color+"╰"+strings.Repeat("─", width-2)+"╯"+reset)
}

func (d *ContentDialog) processCommandRows(code string, width, limit int) []string {
	// Leave enough log rows even for chained commands and multiline scripts.
	budget := max(1, (limit-processDetailMinHeight)/3)
	theme := d.View.Painter.Theme
	p := &d.process
	if p.commandRows != nil && p.command == code && p.commandTheme == theme &&
		p.commandWidth == width && p.commandBudget == budget {
		return p.commandRows
	}
	p.command, p.commandTheme, p.commandWidth, p.commandBudget = code, theme, width, budget
	safe := textsanitize.Safe(code, false)
	label := boldProcessLabel("Command:") + " "
	var rows []string
	if len(safe) <= width*budget {
		for i, row := range d.View.Painter.Highlight("bash", safe) {
			if i == 0 {
				row = label + row
			}
			rows = append(rows, terminalui.Wrap(row, width, true)...)
		}
	}
	if len(rows) == 0 || len(rows) > budget {
		first, _, multiline := strings.Cut(safe, "\n")
		if multiline {
			first += " …"
		}
		short := ansi.Truncate(first, width-ansi.StringWidth(label), "…")
		rows = []string{label + strings.Join(d.View.Painter.Highlight("bash", short), "")}
	}
	p.commandRows = rows
	return rows
}

func (d *ContentDialog) layoutProcess(width int) {
	page := d.Pages[d.Page]
	d.StopAvailable = page.Process != nil
	if !d.ProcessDetail {
		d.Footer = "↑↓ select · Enter view · Esc close"
		if d.StopAvailable {
			d.Footer = "↑↓ select · Enter view · x stop · Esc close"
		}
		if width < 44 {
			d.Footer = "↑↓ select · Enter view · Esc"
			if d.StopAvailable {
				d.Footer = "↑↓ · Enter view · x stop · Esc"
			}
		}
		document := terminalui.DialogPage{Title: "Background"}
		active := 0
		for _, item := range d.Pages {
			if item.Running {
				active++
			}
		}
		document.Detail = fmt.Sprintf("%d active shells", active)
		if len(d.Pages) > 1 {
			document.Detail += fmt.Sprintf(" · %d / %d", d.Page+1, len(d.Pages))
		}
		if page.Source == 0 {
			document.Add(terminalui.DialogLine{Text: "No running processes"})
		} else {
			for i, item := range d.Pages {
				command := strings.Join(strings.Fields(textsanitize.Safe(item.Code, false)), " ")
				owner := textsanitize.Safe(pathdisplay.AgentDisplayName(item.Owner), false)
				status := processStatus(item)
				command = strings.Join(d.View.Painter.Highlight("bash", command), " ")
				label := ansi.Truncate(owner+" · "+command, max(1, width-len(status)-5), "…")
				label += strings.Repeat(" ", max(1, width-ansi.StringWidth(label)-len(status)-3)) + status
				if i == d.Page {
					label = d.View.Painter.Theme.Accent() + "❯ " + terminalui.Reset + label
				} else {
					label = "  " + label
				}
				document.Add(terminalui.DialogLine{Text: label})
			}
		}
		d.SetDocument(document, width)
		d.follow = false
		d.top = max(0, min(d.top, d.Page))
		if d.rows > 0 && d.Page >= d.top+d.rows {
			d.top = d.Page - d.rows + 1
		}
		return
	}
	d.Footer = "← back · ↑↓ scroll · / find · v invocation · y copy · Esc"
	if d.process.invocation {
		d.Footer = "←/v output · ↑↓ scroll · y copy invocation · Esc"
	}
	if d.StopAvailable && !d.process.invocation {
		d.Footer = "x stop · " + d.Footer
	}
	if width-6 < ansi.StringWidth(d.Footer) && !d.process.invocation {
		d.Footer = "← back · v invocation · Esc"
		if d.StopAvailable {
			d.Footer = "x stop · " + d.Footer
		}
	}
	width -= 2 // The two output-frame edges are not selectable text.
	key := contentDialogKey{id: page.ID, revision: page.Revision, width: width, version: page.OutputVersion, retained: page.Retained, theme: d.View.Painter.Theme}
	key.output = d.Output != nil && d.Output.Page == page.ID && d.Output.Version == page.OutputVersion
	if key == d.laidKey {
		return
	}
	if d.process.invocation {
		source := cmp.Or(page.DetailCode, page.Code)
		document := terminalui.DialogPage{Text: source}
		for i, row := range d.View.Painter.Highlight("bash", textsanitize.Safe(source, false)) {
			document.Add(terminalui.DialogLine{Number: i + 1, Gutter: "│", Text: row})
		}
		d.SetDocument(document, width)
		d.laidKey = key
		return
	}
	// Use the shared retained-output renderer, but keep metadata out of its
	// scrollable, searchable and selectable document.
	value := page.Value
	value.Code, value.DetailCode = "", ""
	block := activityui.Block{Value: value}
	if key.output {
		block.OutputSnapshot = &d.Output.View
	}
	document := d.View.Painter.DialogPage(block, width)
	document.Live = page.Running
	lines, dropped := len(page.Tail), page.TailOmitted
	if block.OutputSnapshot != nil && !block.OutputSnapshot.Released {
		lines, dropped = len(block.OutputSnapshot.Lines), block.OutputSnapshot.Dropped
	}
	previous := d.process.outputLines
	d.process.outputLines = lines + dropped
	if previous == 0 && d.follow {
		d.process.seenLines = d.process.outputLines
	}
	// If retention moved the beginning of the stream, keep the same numbered
	// row when possible, rather than following its old index into new output.
	number, wrap := 0, 0
	if !d.follow && d.process.dropped != dropped && d.laidKey.id == key.id && len(d.starts) > 0 {
		i := sort.SearchInts(d.starts[1:], d.top+1)
		if i < len(d.Document.Lines) {
			number, wrap = d.Document.Lines[i].Number, d.top-d.starts[i]
		}
	}
	d.process.dropped = dropped
	if d.process.outputLines == 0 && document.Text == "" {
		document.Text = page.Code
	}
	d.SetDocument(document, width)
	if number > 0 {
		i := sort.Search(len(document.Lines), func(i int) bool {
			return document.Lines[i].Number >= number
		})
		if i < len(document.Lines) {
			d.top = d.starts[i]
			if document.Lines[i].Number == number {
				d.top += min(wrap, d.starts[i+1]-d.starts[i]-1)
			}
		}
	}
	d.laidKey = key
}

func (d *ContentDialog) toggleProcessInvocation() {
	if d.process.invocation {
		log := d.process.logState
		d.top, d.match, d.follow, d.Typing = log.top, log.match, log.follow, log.typing
		d.missed, d.draft, d.query = log.missed, log.draft, log.query
	} else {
		d.process.logState = dialogPageState{d.top, d.match, d.follow, d.Typing, d.missed, d.draft, d.query}
		d.top, d.match, d.follow, d.missed, d.draft, d.query = 0, -1, false, false, "", ""
	}
	d.process.invocation = !d.process.invocation
	d.Invalidate()
}

func (d *ContentDialog) processKey(key string) DialogIntent {
	if len(d.Pages) == 0 {
		return d.Dialog.Key(key)
	}
	if key == "x" || key == "s" {
		if process := d.Pages[d.Page].Process; process != nil {
			return DialogIntent{Action: DialogStopProcess, Process: new(*process)}
		}
		return DialogIntent{}
	}
	if d.ProcessDetail {
		switch key {
		case "v":
			d.toggleProcessInvocation()
			return DialogIntent{}
		case "\x1b[D", "h":
			if d.process.invocation {
				d.toggleProcessInvocation()
				return DialogIntent{}
			}
			d.process = processPane{}
			d.ProcessDetail = false
			d.top, d.follow = 0, false
			d.Invalidate()
			return DialogIntent{}
		case "\r", " ":
			return DialogIntent{Action: DialogClose}
		case "\x1b[C", "l":
			return DialogIntent{}
		}
		intent := d.Dialog.Key(key)
		switch key {
		case "\x1b[F", "\x1b[4~", "G", "\x1b[B", "j", "\x1b[6~":
			d.observeProcessTail()
		}
		return intent
	}
	switch key {
	case "\x1b", "q", "\x03", "\x1b[D", "h":
		return DialogIntent{Action: DialogClose}
	case "\x1b[A", "k":
		d.ShowPage(d.Page - 1)
	case "\x1b[B", "j":
		d.ShowPage(d.Page + 1)
	case "\x1b[5~":
		d.ShowPage(max(0, d.Page-max(1, d.rows)))
	case "\x1b[6~":
		d.ShowPage(min(len(d.Pages)-1, d.Page+max(1, d.rows)))
	case "\x1b[H", "g":
		d.ShowPage(0)
	case "\x1b[F", "G":
		d.ShowPage(len(d.Pages) - 1)
	case "\r", "\x1b[C":
		if d.Pages[d.Page].Source != 0 {
			d.ProcessDetail = true
			d.top, d.follow = 0, true
			d.match, d.missed, d.draft, d.query = -1, false, "", ""
			d.Invalidate()
		}
	}
	return DialogIntent{}
}

func (d *ContentDialog) observeProcessTail() {
	if !d.process.invocation && d.top >= d.bottom() {
		d.process.seenLines = d.process.outputLines
	}
}

func (d *ContentDialog) processMouse(button, x, y int, release bool) DialogIntent {
	if release {
		return DialogIntent{}
	}
	switch button &^ 28 {
	case 64:
		return d.processKey("\x1b[A")
	case 65:
		return d.processKey("\x1b[B")
	case 0:
		if d.rect.Contains(x, y) && y >= d.rect.Y+d.Chrome()-1 && y < d.rect.Y+d.Chrome()-1+d.rows {
			index := y - d.rect.Y - d.Chrome() + 1 + d.top
			if index < len(d.Pages) {
				d.ShowPage(index)
				return d.processKey("\r")
			}
			return DialogIntent{}
		}
	}
	return d.Dialog.Mouse(button, x, y, release)
}
