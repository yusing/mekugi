package router

import (
	"fmt"
	"slices"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Colors are allocated in observation order and remain stable for this viewer.
// The status shape retains its meaning; the role needs no additional cell.
func (v *liveActivityView) roleColor(role string) string {
	if v.roleColors == nil {
		v.roleColors = make(map[string]string)
	}
	if color := v.roleColors[role]; color != "" {
		return color
	}
	palette := []int{75, 214, 141, 114, 203, 80, 180, 217, 111, 149, 220, 159}
	index := len(v.roleColors)
	for color := 16; color < 232; color++ {
		if !slices.Contains(palette, color) {
			palette = append(palette, color)
		}
	}
	color := palette[index%len(palette)]
	style := fmt.Sprintf("\x1b[38;5;%dm", color)
	v.roleColors[role] = style
	return style
}

func (v *liveActivityView) roleLegend() string {
	seen := make(map[string]bool)
	var parts []string
	for _, agent := range v.agents {
		role := liveActivityRole(agent)
		if role == "" || seen[role] {
			continue
		}
		seen[role] = true
		parts = append(parts, v.roleColor(role)+"● "+role+liveActivityReset)
	}
	return strings.Join(parts, "  ")
}

func (u *terminalUI) statusLines() (status, legend string) {
	// Numbered tabs name each pane by its Ctrl-B digit and mark the focused one.
	titles := []string{"Codex", "Diff", "Agents", "Roster"}
	if u.main != nil {
		titles = []string{"Main", "Diff", "Activity", "Agents"}
	}
	var tabs []string
	for i, title := range titles {
		tab := fmt.Sprintf(" %d %s ", i+1, title)
		if i == u.focus {
			tab = "\x1b[7;1m" + tab + liveActivityReset
		} else {
			tab = liveActivityDim + tab + liveActivityUndim
		}
		tabs = append(tabs, tab)
	}
	hints := "Ctrl-B + 1-4 focus · ←↑↓→ resize · [/] files"
	if u.prefix {
		hints = "1-4 focus · ←↑↓→ resize · [/] files · PgUp/PgDn history · Ctrl-B sends prefix"
	}
	status = strings.Join(tabs, "") + "  " + liveActivityDim + hints + liveActivityUndim
	if u.prefix {
		status = strings.Join(tabs, "") + "  " + liveActivityAmber + "Ctrl-B" + liveActivityReset + " " + hints
	}
	if u.activityOpen {
		legend = u.agents.roleLegend()
	}
	if legend != "" && ansi.StringWidth(status)+2+ansi.StringWidth(legend) <= u.width {
		status += "  " + legend
		legend = ""
	}
	return status, legend
}
