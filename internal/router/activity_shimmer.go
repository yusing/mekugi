package router

func (v *liveActivityView) hasLiveReasoning() bool {
	if !v.childrenOnly {
		return false
	}
	for _, agent := range v.agents {
		if agent.Responding {
			if i := v.latest(agent.Name); i >= 0 && v.entries[i].Kind == "reasoning" {
				return true
			}
		}
	}
	return false
}
