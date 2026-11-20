package terminalsession

import (
	"cmp"
	"fmt"
	"strings"

	pathterminal "github.com/yusing/mekugi/libraries/pathdisplay/terminal"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/terminalui"
	"github.com/yusing/mekugi/libraries/textsanitize"
)

// A compact instrument panel: model settings, quota gauges, then account and
// workspace details, in the docked panel style: an accent heading with its
// close control, an inset body and dim keys. The heading and keys stay fixed
// while the body scrolls.
// Source: apps/mekugi/internal/router/app_server_status_render.go@630ce384.
func (p *StatusPanel) Render(painter *terminalui.Painter, width, height int) []string {
	p.Rect, p.Close = Rect{}, Rect{}
	frame := make([]string, height)
	if width < 1 || height < 1 {
		return frame
	}
	const reset = "\x1b[0m"
	const bold = "\x1b[1m"
	const dim = "\x1b[2m"
	accent, warning, danger := painter.Theme.Accent(), painter.Theme.Warning(), painter.Theme.Danger()
	panelWidth := min(82, width)
	if width >= 44 {
		panelWidth = min(panelWidth, width-4)
	}
	x := (width - panelWidth) / 2
	boxed := panelWidth >= 10 && height >= 5
	contentWidth := max(1, panelWidth-4)
	if !boxed {
		contentWidth = max(1, panelWidth-2)
	}
	labelLimit, valueColumn := 14, 16
	if p.body.Metrics != nil {
		labelLimit, valueColumn = 26, 28
	}
	type line struct {
		label, text string
		heading     bool // The label is a group title beside its column headings.
	}
	var lines []line
	appendText := func(text string) {
		for _, part := range panelWrap(text, contentWidth) {
			lines = append(lines, line{text: part})
		}
	}
	// Comparison cells align across the whole panel, so columns stay fixed
	// while scrolling. Rows keep the label column when the cells fit beside it.
	var cellWidths []int
	compareLabel := 0
	for _, field := range p.Fields {
		if len(field.Values) == 0 {
			continue
		}
		for len(cellWidths) < len(field.Values) {
			cellWidths = append(cellWidths, 0)
		}
		for i, value := range field.Values {
			cellWidths[i] = max(cellWidths[i], ansi.StringWidth(panelText(value)))
		}
		compareLabel = max(compareLabel, ansi.StringWidth(panelText(cmp.Or(field.Label, field.Group))))
	}
	cellsWidth := 0
	for _, width := range cellWidths {
		cellsWidth += width + 2
	}
	compareInline := contentWidth >= compareLabel+cellsWidth
	cells := func(values []string) string {
		parts := make([]string, len(values))
		for i, value := range values {
			value = panelText(value)
			parts[i] = strings.Repeat(" ", max(0, cellWidths[i]-ansi.StringWidth(value))) + value
		}
		return strings.Join(parts, "  ")
	}
	groups := p.Groups
	if groups == nil {
		groups = []string{"Model", "Usage", "Usage limit resets", "Account", "Session", "Permissions"}
	}
	for _, group := range groups {
		started := false
		for index, field := range p.Fields {
			if field.Group != group {
				continue
			}
			if field.Heading {
				if len(lines) > 0 {
					lines = append(lines, line{})
				}
				started = true
				if compareInline {
					lines = append(lines, line{label: group, text: dim + cells(field.Values) + reset, heading: true})
				} else {
					appendText(bold + accent + group + reset)
					appendText(dim + cells(field.Values) + reset)
				}
				continue
			}
			if !started {
				if len(lines) > 0 {
					lines = append(lines, line{})
				}
				title := bold + accent + group + reset
				rule := max(0, contentWidth-ansi.StringWidth(group)-2)
				appendText(title + "  " + dim + strings.Repeat("─", rule) + reset)
				started = true
			}
			label, value, detail := panelText(field.Label), panelText(field.Value), panelText(field.Detail)
			if len(field.Values) > 0 {
				if compareInline {
					lines = append(lines, line{label: label, text: cells(field.Values)})
				} else {
					appendText(dim + label + reset)
					appendText(cells(field.Values))
				}
				continue
			}
			if field.Path {
				value = terminalui.StyledPath(value)
			}
			if field.Remaining != nil {
				percent := max(0, min(100, *field.Remaining))
				color := accent
				if percent <= 10 {
					color = danger
				} else if percent <= 25 {
					color = warning
				}
				gap := contentWidth - ansi.StringWidth(label) - ansi.StringWidth(value)
				if gap >= 2 {
					appendText(label + strings.Repeat(" ", gap) + bold + color + value + reset)
				} else {
					appendText(label)
					appendText(bold + color + value + reset)
				}
				filled := int(percent*float64(contentWidth)/100 + .5)
				appendText(color + strings.Repeat("━", filled) + reset + dim + strings.Repeat("─", contentWidth-filled) + reset)
				if detail != "" {
					appendText(dim + detail + reset)
				}
				continue
			}
			style := ""
			if field.Alert {
				style = danger
			} else if label == "" {
				style = dim
			} else if label == "Model" {
				style = bold + accent
			}
			wrapValue := func(width int) []string {
				if field.Path {
					row := pathterminal.Truncate(value, width)
					if painter.CopySource && !painter.LayoutOnly {
						row = terminalui.AttachCopy(row, []terminalui.CopySpan{{ID: uint64(index + 1), Prefix: ansi.Strip(value), Width: ansi.StringWidth(row)}})
					}
					return []string{row}
				}
				return painter.CopyWrapped(panelWrap(value, width), terminalui.CopyFragment{ID: uint64(index + 1), Text: ansi.Strip(value)}, 0)
			}
			// Reserve a bounded label column for wrapping. Actual alignment is measured
			// below from visible rows only, so off-screen labels cannot widen it.
			if contentWidth >= valueColumn+22 && ansi.StringWidth(label) <= labelLimit && label != "" {
				for i, part := range wrapValue(contentWidth - valueColumn) {
					item := line{text: style + part + reset}
					if i == 0 {
						item.label = label
					}
					// Continuation rows use the same value column.
					if i > 0 {
						item.label = " "
					}
					lines = append(lines, item)
				}
			} else {
				if label != "" {
					appendText(dim + label + reset)
				}
				for _, part := range wrapValue(contentWidth) {
					lines = append(lines, line{text: style + part + reset})
				}
			}
		}
	}
	chrome := 2
	if boxed {
		chrome = 4
	}
	panelHeight := min(height, len(lines)+chrome)
	p.Rows = max(0, panelHeight-chrome)
	p.Top = min(p.Top, max(0, len(lines)-max(1, p.Rows)))
	y := (height - panelHeight) / 2
	put := func(row int, text string) {
		if row >= 0 && row < height {
			frame[row] = strings.Repeat(" ", x) + ansi.Truncate(text, panelWidth, "")
		}
	}
	pad := func(text string, n int) string {
		text = ansi.Truncate(text, n, "")
		return text + strings.Repeat(" ", max(0, n-ansi.StringWidth(text)))
	}
	if boxed {
		heading := terminalui.PanelHeading(accent, cmp.Or(p.Title, "Session status"), "", terminalui.DialogClose, panelWidth-4)
		put(y, "  "+heading)
		if strings.Contains(ansi.Strip(heading), terminalui.DialogClose) {
			p.Close = Rect{x + panelWidth - terminalui.DialogCloseOffset, y, terminalui.DialogCloseWidth, 1}
		}
	} else {
		put(y, bold+accent+cmp.Or(p.Title, "Status")+reset)
	}
	start := y + 1
	if boxed {
		start++
	}
	p.Rect = Rect{x + 1, start, contentWidth, p.Rows}
	if boxed {
		p.Rect.X = x + 2
	}
	visible := lines[p.Top:min(len(lines), p.Top+p.Rows)]
	labelWidth := 0
	for _, row := range visible {
		labelWidth = max(labelWidth, ansi.StringWidth(row.label))
	}
	for i, row := range visible {
		text := row.text
		if row.heading {
			text = bold + accent + pad(row.label, labelWidth) + reset + "  " + text
		} else if row.label != "" {
			prefix := dim + pad(strings.TrimSpace(row.label), labelWidth) + reset + "  "
			if label := strings.TrimSpace(row.label); label != "" {
				_, spans := terminalui.ExtractCopy(text)
				if len(spans) > 0 {
					prefix = terminalui.AttachCopy(prefix, []terminalui.CopySpan{{ID: spans[0].ID, Text: label + "  ", Offset: -len(label) - 2, Width: ansi.StringWidth(prefix)}})
				}
			}
			text = prefix + text
		}
		if boxed {
			put(start+i, "  "+pad(text, contentWidth))
		} else {
			put(start+i, " "+text)
		}
	}
	controls := "↑↓ scroll · pgup/pgdn page · esc close"
	if panelWidth >= 70 {
		controls = "↑↓ scroll · pgup/pgdn page · drag to select · esc close"
	}
	if panelWidth < 44 {
		controls = "↑↓ scroll · esc close"
	}
	if p.Controls != "" {
		controls = p.Controls
		if panelWidth < 54 {
			controls = "←→ views · ↑↓ scroll · esc close"
		}
		if panelWidth < 36 {
			controls = "←→ · ↑↓ · esc close"
		}
	}
	if len(lines) > p.Rows && panelWidth >= 54 {
		controls += fmt.Sprintf(" · %d–%d / %d", p.Top+1, p.Top+len(visible), len(lines))
	}
	if boxed {
		put(y+panelHeight-1, "  "+dim+ansi.Truncate(controls, panelWidth-4, "…")+reset)
	} else if panelHeight > 1 {
		put(y+panelHeight-1, dim+"esc close"+reset)
	}
	return frame
}

func panelWrap(text string, width int) []string {
	return strings.Split(ansi.Wrap(text, max(1, width), ""), "\n")
}
func panelText(text string) string {
	return strings.ReplaceAll(textsanitize.Safe(text, false), "\n", " ")
}
