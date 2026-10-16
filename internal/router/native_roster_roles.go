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
	u.agents.Agents = slices.Clone(u.agents.Agents)
	for i := range u.agents.Agents {
		agent := &u.agents.Agents[i]
		host := u.session.agent(agent.Name)
		if host == nil {
			continue
		}
		agent.Role = host.Role
	}
}
