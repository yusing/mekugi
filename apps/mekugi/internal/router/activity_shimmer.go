package router

func (v *liveActivityView) hasLiveStatus() bool {
	if !v.childrenOnly {
		return false
	}
	for _, agent := range v.Agents {
		if agent.Responding {
			source, owner := v, agent.Name
			if agent.Name == "/root" && v.mainView != nil {
				source, owner = v.mainView, "Main"
			}
			if i := source.Latest(owner); i >= 0 && source.Entries[i].StatusAnimating() {
				return true
			}
		}
	}
	return false
}
