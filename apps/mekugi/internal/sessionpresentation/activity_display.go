package sessionpresentation

import "cmp"

func (m *Model) DisplayingChild() bool {
	return m.view.Displayed.ID != m.view.Main.ID
}

// Source: apps/mekugi/internal/router/session_presentation.go:88:103@e596fd5f.
// Roster keys and hit targets resolve to stable thread identities before
// admission. The highlighted row remains renderer-local.
func (m *Model) ActivityTarget(caller string, source *ActivitySource) Thread {
	if caller != "" && caller == m.view.SideThread.Caller {
		return m.view.SideThread
	}
	target := m.view.Main
	if caller != "/root" && caller != "" {
		target = Thread{ID: caller, Caller: caller}
		for _, row := range source.Model.Roster() {
			if row.Agent.Name == caller {
				target.ID = cmp.Or(row.Agent.Thread, caller)
				break
			}
		}
	}
	return target
}
