package launch

import (
	"slices"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
)

// Refresh roles from host metadata without guessing from task names or colors.
func refreshRosterRoles(session *sessionpresentation.Attachment, source *sessionpresentation.ActivitySource) {
	if source == nil {
		return
	}
	// History restoration can share this slice with the host session.
	source.Agents = slices.Clone(source.Agents)
	for i := range source.Agents {
		agent := &source.Agents[i]
		host := session.Agent(agent.Name)
		if host == nil {
			continue
		}
		agent.Role = host.Role
	}
}
