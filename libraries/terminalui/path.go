package terminalui

import "strings"

// Source: internal/ui/activity/paint.go:82:87@[8a494004]
// StyledPath emphasizes the filename in an already resolved display path.
func StyledPath(path string) string {
	if i := strings.LastIndex(strings.TrimSuffix(path, "/"), "/"); i >= 0 {
		return Dim + path[:i+1] + Undim + "\x1b[1m" + path[i+1:] + Undim
	}
	return "\x1b[1m" + path + Undim
}
