package router

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
)

// A compact instrument panel: model settings, quota gauges, then account and
// workspace details. The border and controls stay fixed while the body scrolls.
func (u *appServerUI) statusPanelFrame(width, height int) []string {
	p := u.statusPanel
	u.mainContentPainted = false
	u.composerRect = terminalRect{}
	p.rect = terminalRect{}
	frame := make([]string, height)
	if width < 1 || height < 1 {
		return frame
	}
	const reset = "\x1b[0m"
	const bold = "\x1b[1m"
	const dim = "\x1b[2m"
	accent, warning, danger := "\x1b[38;2;104;174;245m", "\x1b[38;2;230;181;94m", "\x1b[38;2;239;117;117m"
	if u.view.painter.Theme == livediff.LightTheme {
		accent, warning, danger = "\x1b[38;2;25;94;164m", "\x1b[38;2;134;86;16m", "\x1b[38;2;178;43;43m"
	}
	panelWidth := min(82, width)
	if width >= 44 {
		panelWidth = min(panelWidth, width-4)
	}
	x := (width - panelWidth) / 2
	boxed := panelWidth >= 10 && height >= 5
	contentWidth := max(1, panelWidth-6)
	if !boxed {
		contentWidth = max(1, panelWidth-2)
	}
	type line struct{ label, text string }
	var lines []line
	appendText := func(text string) {
		for _, part := range pickerWrap(text, contentWidth) {
			lines = append(lines, line{text: part})
		}
	}
	for _, group := range []string{"Model", "Usage", "Account", "Session", "Permissions"} {
		started := false
		for _, field := range p.fields {
			if field.group != group {
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
			label, value, detail := pickerText(field.label), pickerText(field.value), pickerText(field.detail)
			if field.remaining != nil {
				percent := max(0, min(100, *field.remaining))
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
			if field.alert {
				style = danger
			} else if label == "" {
				style = dim
			} else if label == "Model" {
				style = bold + accent
			}
			// Reserve a bounded label column for wrapping. Actual alignment is measured
			// below from visible rows only, so off-screen labels cannot widen it.
			if contentWidth >= 38 && ansi.StringWidth(label) <= 14 && label != "" {
				for i, part := range pickerWrap(value, contentWidth-16) {
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
				appendText(style + value + reset)
			}
		}
	}
	chrome := 2
	if boxed {
		chrome = 4
	}
	panelHeight := min(height, len(lines)+chrome)
	p.rows = max(0, panelHeight-chrome)
	p.top = min(p.top, max(0, len(lines)-max(1, p.rows)))
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
		title := ansi.Truncate(" Session status ", panelWidth-2, "")
		closeHint := " Esc close "
		if panelWidth < 36 {
			closeHint = ""
		}
		put(y, dim+"╭"+reset+bold+accent+title+reset+dim+strings.Repeat("─", max(0, panelWidth-2-ansi.StringWidth(title+closeHint)))+reset+closeHint+dim+"╮"+reset)
		put(y+1, dim+"│"+reset+strings.Repeat(" ", panelWidth-2)+dim+"│"+reset)
	} else {
		put(y, bold+accent+"Status"+reset)
	}
	start := y + 1
	if boxed {
		start++
	}
	p.rect = terminalRect{x + 1, start, contentWidth, p.rows}
	if boxed {
		p.rect.x = x + 3
	}
	visible := lines[p.top:min(len(lines), p.top+p.rows)]
	labelWidth := 0
	for _, row := range visible {
		labelWidth = max(labelWidth, ansi.StringWidth(row.label))
	}
	for i, row := range visible {
		text := row.text
		if row.label != "" {
			text = dim + pad(strings.TrimSpace(row.label), labelWidth) + reset + "  " + text
		}
		if boxed {
			put(start+i, dim+"│"+reset+"  "+pad(text, contentWidth)+"  "+dim+"│"+reset)
		} else {
			put(start+i, " "+text)
		}
	}
	controls := "↑↓ scroll  PgUp/PgDn"
	if panelWidth >= 70 {
		controls += "  drag to select"
	}
	if panelWidth < 40 {
		controls = "↑↓ scroll  Esc close"
	}
	if len(lines) > p.rows && panelWidth >= 54 {
		controls += fmt.Sprintf("  %d–%d / %d", p.top+1, p.top+len(visible), len(lines))
	}
	if boxed {
		put(y+panelHeight-2, dim+"│"+reset+strings.Repeat(" ", panelWidth-2)+dim+"│"+reset)
		footer := " " + controls + " "
		footer = ansi.Truncate(footer, panelWidth-3, "")
		put(y+panelHeight-1, dim+"╰─"+reset+dim+footer+strings.Repeat("─", max(0, panelWidth-3-ansi.StringWidth(footer)))+"╯"+reset)
	} else if panelHeight > 1 {
		put(y+panelHeight-1, dim+"Esc close"+reset)
	}
	return frame
}
