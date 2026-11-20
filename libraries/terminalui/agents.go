package terminalui

import (
	"fmt"
	"hash/fnv"
	"strings"

	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/pathdisplay"
)

// Source: internal/ui/activity/agents.go@[8a494004]
// Agent colors derive from the canonical path alone, so the agents pane and
// the live diff pane agree without sharing state, across reconnects too.
// Hues stay apart from each other and from the cyan accent that marks Main.
// Dark backgrounds take the bright set, light ones the deep set, and an
// unknown background the mid-tones legible on either.
var liveAgentPalette = [...]struct {
	dark, terminal uint8
	light          [3]uint8
}{
	{209, 166, [3]uint8{176, 76, 0}},
	{170, 133, [3]uint8{153, 51, 153}},
	{111, 68, [3]uint8{31, 99, 186}},
	{150, 107, [3]uint8{56, 118, 29}},
	{222, 136, [3]uint8{128, 96, 0}},
	{183, 140, [3]uint8{111, 66, 193}},
	{174, 131, [3]uint8{176, 48, 96}},
}

// AgentColor returns a bold SGR prefix; the root uses the caller's own style.
func AgentColor(theme diffrender.Theme, name string) string {
	name = pathdisplay.AgentCanonicalName(name)
	if name == "" || name == "/root" {
		return ""
	}
	hash := fnv.New32a()
	hash.Write([]byte(name))
	color := liveAgentPalette[hash.Sum32()%uint32(len(liveAgentPalette))]
	switch theme {
	case diffrender.LightTheme:
		return fmt.Sprintf("\x1b[1;38;2;%d;%d;%dm", color.light[0], color.light[1], color.light[2])
	case diffrender.DarkTheme:
		return fmt.Sprintf("\x1b[1;38;5;%dm", color.dark)
	default:
		return fmt.Sprintf("\x1b[1;38;5;%dm", color.terminal)
	}
}

// DimAgentColor keeps the agent foreground and marks it faint for the output policy.
func DimAgentColor(theme diffrender.Theme, name string) string {
	color := AgentColor(theme, name)
	if color == "" {
		return Dim
	}
	return strings.Replace(color, "1;", "2;", 1)
}
