package terminalui

import (
	"fmt"
	"hash/fnv"
	"strings"
)

// Source: internal/ui/activity/agents.go@[8a494004]
// Agent colors derive from the canonical path alone, so the agents pane and
// the live diff pane agree without sharing state, across reconnects too.
var liveAgentPalette = [...]struct{ normal, dim uint8 }{
	{39, 31}, {170, 133}, {38, 30}, {99, 61},
	{37, 29}, {133, 96}, {74, 67},
}

// AgentColor returns an SGR prefix; the root uses the caller's own style.
func AgentColor(name string) string {
	if name == "agent:main" || strings.HasPrefix(name, "agent:main/") {
		name = "/root" + strings.TrimPrefix(name, "agent:main")
	}
	if name == "" || name == "/root" || name == "Main" {
		return ""
	}
	hash := fnv.New32a()
	hash.Write([]byte(name))
	return fmt.Sprintf("\x1b[1;38;5;%dm", liveAgentPalette[hash.Sum32()%uint32(len(liveAgentPalette))].normal)
}

// DimAgentColor keeps the agent foreground and marks it faint for the output policy.
func DimAgentColor(name string) string {
	color := AgentColor(name)
	if color == "" {
		return Dim
	}
	return strings.Replace(color, "1;", "2;", 1)
}
