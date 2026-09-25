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
	title := []string{"CODEX", "DIFF", "AGENTS", "ROSTER"}[u.focus]
	status = " " + title + " · Ctrl-B 1/2/3/4 focus · ←/→ width · ↑/↓ height · [/] files"
	if u.prefix {
		status = " Layout: 1/2/3/4 focus · arrows resize · [/] files · PgUp/PgDn Codex history · Ctrl-B sends prefix"
	}
	if u.activityOpen {
		legend = u.agents.roleLegend()
	}
	status = liveActivityDim + status + liveActivityUndim
	if legend != "" && ansi.StringWidth(status)+2+ansi.StringWidth(legend) <= u.width {
		status += "  " + legend
		legend = ""
	}
	return status, legend
}
