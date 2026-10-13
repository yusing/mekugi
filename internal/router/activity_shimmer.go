package router

func (r liveActivityRecord) statusAnimating() bool {
	if r.native == nil || !r.native.live || len(r.blocks) == 0 {
		return false
	}
	block := r.blocks[len(r.blocks)-1]
	return block.Live && (block.Kind == "summary" || block.Kind == "progress")
}

func (v *liveActivityView) hasLiveStatus() bool {
	if !v.childrenOnly {
		return false
	}
	for _, agent := range v.agents {
		if agent.Responding {
			source, owner := v, agent.Name
			if agent.Name == "/root" && v.mainView != nil {
				source, owner = v.mainView, "Main"
			}
			if i := source.latest(owner); i >= 0 && source.entries[i].statusAnimating() {
				return true
			}
		}
	}
	return false
}
