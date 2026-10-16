package terminalui

import "github.com/yusing/mekugi/libraries/diffrender"

// Source: internal/ui/activity/animation.go:15:20@[b5ba67e3] reported terminal colors.
// Colors are the default text and background colors the terminal
// reported through OSC 10 and OSC 11.
type Colors struct {
	Foreground, Background       diffrender.RGB
	HasForeground, HasBackground bool
}

// Source: internal/ui/activity/paint.go:19:23@[8a494004]
const (
	Green = "\x1b[38;5;114m"
	Red   = "\x1b[38;5;203m"
	Amber = "\x1b[38;5;214m"
)
