package activity

import (
	"fmt"
	"hash/fnv"
	"strings"
)

// Agent colors derive from the canonical path alone, so the agents pane and
// the live diff pane agree without sharing state, across reconnects too.
var liveAgentPalette = [...]struct{ normal, dim uint8 }{
	{39, 31}, {170, 133}, {38, 30}, {99, 61},
	{37, 29}, {133, 96}, {74, 67},
}

// liveAgentColor returns an SGR prefix; the root uses the caller's own style.
func Color(name string) string {
	if name == "" || name == "/root" {
		return ""
	}
	hash := fnv.New32a()
	hash.Write([]byte(name))
	return fmt.Sprintf("\x1b[1;38;5;%dm", liveAgentPalette[hash.Sum32()%uint32(len(liveAgentPalette))].normal)
}

// DimColor keeps the agent foreground and marks it faint for the output policy.
func DimColor(name string) string {
	if name == "" || name == "/root" {
		return Dim
	}
	return strings.Replace(Color(name), "1;", "2;", 1)
}

// agentDisplayName is the one user-facing form of a canonical agent path:
// main for the root, the path below it otherwise (worker, or a/worker when
// nested). Model-visible text keeps canonical paths, since agents address
// each other by them.
func AgentDisplayName(name string) string {
	if name == "/root" {
		return "main"
	}
	return strings.TrimPrefix(name, "/root/")
}
