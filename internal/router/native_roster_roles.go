package router

import (
	"maps"
	"slices"
)

// Role evidence augments presentation only. Host metadata wins; authenticated
// spawn evidence fills omissions without guessing from task names or colors.
func (u *appServerUI) refreshRosterRoles() {
	if u.agents == nil {
		return
	}
	// History restoration can share this slice with the host session.
	u.agents.agents = slices.Clone(u.agents.agents)
	roles := make(map[string]journalSpawnRole)
	for _, sink := range []*nativeJournalSink{u.journal, u.unscopedJournal} {
		if sink == nil {
			continue
		}
		sink.mu.Lock()
		view := sink.mounted
		if view == nil {
			view = sink.tree
		}
		if view != nil {
			maps.Copy(roles, view.SpawnRoles)
		}
		sink.mu.Unlock()
	}
	for i := range u.agents.agents {
		agent := &u.agents.agents[i]
		host := u.session.agent(agent.Name)
		if host == nil {
			continue
		}
		agent.Role = host.Role
		if agent.Role == "" {
			if evidence := roles[agent.Name]; !evidence.Conflicted {
				agent.Role = evidence.Role
			}
		}
	}
}
