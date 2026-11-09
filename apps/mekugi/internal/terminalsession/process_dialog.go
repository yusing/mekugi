package terminalsession

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/pathdisplay"
	"github.com/yusing/mekugi/libraries/terminalui"
	"github.com/yusing/mekugi/libraries/textsanitize"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

func (d *ContentDialog) processStatus(index int) string {
	page := d.Pages[index]
	switch {
	case page.Running:
		return "running"
	case page.Exited:
		return fmt.Sprintf("exit %d", page.ExitCode)
	default:
		return "completed"
	}
}

// PaintProcesses paints an inline surface below the composer, without a modal
// backdrop or dialog border. Navigation and retained-output selection are shared
// with other content views.
func (d *ContentDialog) PaintProcesses(rows []string, rect Rect) {
	if rect.W < 12 || rect.H < d.Chrome()+1 {
		d.rect, d.Selection = Rect{}, nil
		return
	}
	if d.rect != rect {
		d.Selection = nil
	}
	d.rect, d.rows, d.theme = rect, rect.H-d.Chrome(), d.View.Painter.Theme
	padding := !d.ProcessDetail && d.rows > 1
	if padding {
		d.rows--
	}
	if d.Selection == nil {
		d.Layout(rect.W - 4)
	}
	d.layoutBody()
	detail := d.Document.Detail
	if len(d.Pages) > 1 {
		detail += fmt.Sprintf(" · %d / %d", d.Page+1, len(d.Pages))
	}
	if d.Document.Live {
		detail += " · ● live"
		if !d.follow {
			detail += " · paused"
		}
	}
	lines := []string{
		d.theme.Accent() + strings.Repeat("─", rect.W-4) + terminalui.Reset,
		d.theme.Accent() + d.Document.Title + terminalui.Reset + "  " + detail,
		"",
	}
	for i, row := range d.body {
		if d.Selection != nil {
			row = d.Selection.Row(d.Selection.DocumentY(i), d.theme)
		}
		lines = append(lines, row)
	}
	if padding {
		lines = append(lines, "")
	}
	footer := d.footer(rect.W)
	if d.Selection != nil {
		footer = " " + SelectionHints.Render()
	}
	lines = append(lines, footer)
	for i, line := range lines {
		if y := rect.Y + i; y >= 0 && y < len(rows) {
			rows[y] += fmt.Sprintf("\x1b[%dG\x1b[0m  %s\x1b[0m", rect.X+1, ansi.Truncate(line, rect.W-4, ""))
		}
	}
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
		if page.Source == 0 {
			document.Add(terminalui.DialogLine{Text: "No running processes"})
		} else {
			for i, item := range d.Pages {
				command := strings.Join(strings.Fields(textsanitize.Safe(item.Code, false)), " ")
				owner := textsanitize.Safe(pathdisplay.AgentDisplayName(item.Owner), false)
				status := d.processStatus(i)
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
	d.Footer = "← back · Esc/Enter/Space close · ↑↓ scroll"
	if d.StopAvailable {
		d.Footer = "← back · Esc/Enter/Space close · x stop"
	}
	if width < 44 {
		d.Footer = "← back · Esc close · ↑↓ scroll"
		if d.StopAvailable {
			d.Footer = "← back · Esc close · x stop"
		}
	}
	key := contentDialogKey{id: page.ID, revision: page.Revision, width: width, version: page.OutputVersion, retained: page.Retained, theme: d.View.Painter.Theme}
	key.output = d.Output != nil && d.Output.Page == page.ID && d.Output.Version == page.OutputVersion
	if key != d.laidKey {
		// Reuse retained-output painting and its release/omission notices, but
		// expose the whole command as metadata rather than a second code block.
		value := page.Value
		value.Code = ""
		block := activityui.Block{Value: value}
		if key.output {
			block.OutputSnapshot = &d.Output.View
		}
		document := d.View.Painter.DialogPage(block, width)
		document.Title = "Shell details"
		if document.Text == "" {
			document.Text = page.Code
		}
		if len(value.Tail) == 0 && value.TailOmitted == 0 && (block.OutputSnapshot == nil || len(block.OutputSnapshot.Lines) == 0 && !block.OutputSnapshot.Released && block.OutputSnapshot.Dropped == 0) {
			document.Lines = []terminalui.DialogLine{{Text: terminalui.Dim + "No output available" + terminalui.Undim}}
		}
		lines := []terminalui.DialogLine{
			{Text: "Status:  " + d.processStatus(d.Page), Wrap: true},
			{},
			{Text: "Command: " + strings.Join(d.View.Painter.Highlight("bash", textsanitize.Safe(page.Code, false)), "\n"), Wrap: true},
			{},
			{Text: "Output:"},
		}
		document.Lines = append(lines, document.Lines...)
		d.SetDocument(document, width)
		d.laidKey = key
	}
	elapsed := activityui.RunElapsed(activityui.Block{Value: page.Value}, d.View.Now())
	if elapsed == "" {
		elapsed = "0s"
	}
	d.Document.Lines[1] = terminalui.DialogLine{Text: "Runtime: " + elapsed}
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
		case "\x1b[D", "h":
			d.ProcessDetail = false
			d.top, d.follow = 0, false
			d.Invalidate()
			return DialogIntent{}
		case "\r", " ":
			return DialogIntent{Action: DialogClose}
		case "\x1b[C", "l":
			return DialogIntent{}
		}
		return d.Dialog.Key(key)
	}
	switch key {
	case "\x1b", "q", "\x03":
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
			d.top, d.follow = 0, d.Pages[d.Page].Running
			d.Invalidate()
		}
	}
	return DialogIntent{}
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
