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
		for _, entry := range v.entries {
			if entry.Kind == "progress" && v.visible(entry.activityPaneEntry) && entry.statusAnimating() {
				return true
			}
		}
		return false
	}
	for _, agents := range [][]activityPaneAgent{v.agents, v.orchestration} {
		for _, agent := range agents {
			if agent.Responding {
				source, owner := v, agent.Name
				if other := v.orchestrationSources[agent.Name]; other != nil {
					if v.orchestrationLabels[agent.Name] != "" || v.orchestrationRetained[agent.Name] {
						continue
					}
					source, owner = other, "/root"
				}
				if owner == "/root" && source.mainView != nil {
					source, owner = source.mainView, "Main"
				}
				if i := source.latest(owner); i >= 0 && source.entries[i].statusAnimating() {
					return true
				}
			}
		}
	}
	return false
}
