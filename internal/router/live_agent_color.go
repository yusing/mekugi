package router

import "hash/fnv"

// Agent colors derive from the canonical path alone, so the agents pane and
// the live diff pane agree without sharing state, across reconnects too.
var liveAgentPalette = []string{"39", "170", "38", "99", "37", "133", "74"}

// liveAgentColor returns an SGR prefix; the root uses the caller's own style.
func liveAgentColor(name string) string {
	if name == "" || name == "/root" {
		return ""
	}
	hash := fnv.New32a()
	hash.Write([]byte(name))
	return "\x1b[1;38;5;" + liveAgentPalette[hash.Sum32()%uint32(len(liveAgentPalette))] + "m"
}
