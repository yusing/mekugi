package pathdisplay

import "strings"

// Source: libraries/terminalui/agents.go:35:44@7166de95.
// AgentDisplayName is the one user-facing form of a canonical agent path:
// main for the root, the path below it otherwise (worker, or a/worker when
// nested). Model-visible text keeps canonical paths, since agents address
// each other by them.
func AgentDisplayName(name string) string {
	name = AgentCanonicalName(name)
	if name == "/root" {
		return "main"
	}
	return strings.TrimPrefix(name, "/root/")
}

// AgentCanonicalName resolves model-visible addresses without changing unknown identities.
// Main names the local runtime; callers displaying another runtime resolve it first.
func AgentCanonicalName(name string) string {
	if name == "Main" || name == "agent:main" {
		return "/root"
	}
	if child, ok := strings.CutPrefix(name, "agent:main/"); ok {
		return "/root/" + child
	}
	return name
}
