package control

import "strings"

// AgentName distinguishes model-visible coordination identifiers from paths.
// Persisted task identities continue to use the canonical /root hierarchy.
func AgentName(task string) string {
	if task == "/root" || strings.HasPrefix(task, "/root/") {
		return "agent:main" + strings.TrimPrefix(task, "/root")
	}
	return task
}

// TaskPath accepts displayed agent names without changing relative aliases or IDs.
func TaskPath(name string) string {
	if name == "agent:main" || strings.HasPrefix(name, "agent:main/") {
		return "/root" + strings.TrimPrefix(name, "agent:main")
	}
	return name
}
