package pathdisplay

import "testing"

func TestAgentDisplayName(t *testing.T) {
	for name, want := range map[string]string{
		"/root": "main", "Main": "main", "agent:main": "main",
		"/root/worker": "worker", "agent:main/worker": "worker",
		"agent:main/worker/reviewer": "worker/reviewer",
		"agent:mainland/worker":      "agent:mainland/worker", "unknown": "unknown",
	} {
		if got := AgentDisplayName(name); got != want {
			t.Errorf("AgentDisplayName(%q) = %q, want %q", name, got, want)
		}
	}
}
