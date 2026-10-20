package terminalview

// Source: apps/mekugi/internal/router/activity_shimmer.go:3:19@[29de53cc].

func (v *View) HasLiveStatus() bool {
	if !v.ChildrenOnly {
		return false
	}
	for _, row := range v.roster {
		agent := row.Agent
		if agent.Responding && len(agent.Summary) > 0 && agent.Summary[len(agent.Summary)-1].StatusAnimating() {
			return true
		}
	}
	return false
}
