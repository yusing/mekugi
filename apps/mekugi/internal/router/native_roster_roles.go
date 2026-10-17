package router

import (
	"slices"
)

// Refresh roles from host metadata without guessing from task names or colors.
func (u *appServerUI) refreshRosterRoles() {
	if u.childSource == nil {
		return
	}
	// History restoration can share this slice with the host session.
	u.childSource.Agents = slices.Clone(u.childSource.Agents)
	for i := range u.childSource.Agents {
		agent := &u.childSource.Agents[i]
		host := u.session.agent(agent.Name)
		if host == nil {
			continue
		}
		agent.Role = host.Role
	}
}
