package router

import (
	"fmt"
	"slices"
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
