package router

import (
	"slices"
)

// Refresh roles from host metadata without guessing from task names or colors.
func (u *appServerUI) refreshRosterRoles() {
	if u.agents == nil {
		return
	}
	// History restoration can share this slice with the host session.
	u.agents.agents = slices.Clone(u.agents.agents)
	for i := range u.agents.agents {
		agent := &u.agents.agents[i]
		host := u.session.agent(agent.Name)
		if host == nil {
			continue
		}
		agent.Role = host.Role
	}
}
