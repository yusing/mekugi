package pathdisplay

import "strings"

// Source: libraries/terminalui/agents.go:35:44@7166de95.
// AgentDisplayName is the one user-facing form of a canonical agent path:
// main for the root, the path below it otherwise (worker, or a/worker when
// nested). Model-visible text keeps canonical paths, since agents address
// each other by them.
func AgentDisplayName(name string) string {
	if name == "/root" {
		return "main"
	}
	return strings.TrimPrefix(name, "/root/")
}
