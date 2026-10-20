package terminalview

// Source: apps/mekugi/internal/router/activity_roles.go:8:28@[29de53cc].

import (
	"fmt"
	"slices"
)

// Colors are allocated in observation order and remain stable for this viewer.
// The status shape retains its meaning; the role needs no additional cell.
func (v *View) RoleColor(role string) string {
	if v.RoleColors == nil {
		v.RoleColors = make(map[string]string)
	}
	if color := v.RoleColors[role]; color != "" {
		return color
	}
	palette := []int{75, 214, 141, 114, 203, 80, 180, 217, 111, 149, 220, 159}
	index := len(v.RoleColors)
	for color := 16; color < 232; color++ {
		if !slices.Contains(palette, color) {
			palette = append(palette, color)
		}
	}
	color := palette[index%len(palette)]
	style := fmt.Sprintf("\x1b[38;5;%dm", color)
	v.RoleColors[role] = style
	return style
}
