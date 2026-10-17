package sessionpresentation

import "cmp"

func (m *Model) DisplayingChild() bool {
	view := m.View()
	return view.Displayed.ID != view.Main.ID
}

// Source: apps/mekugi/internal/router/session_presentation.go:88:103@e596fd5f.
// Roster keys and hit targets resolve to stable thread identities before
// admission. The highlighted row remains renderer-local.
func (m *Model) ActivityTarget(caller string, source *ActivitySource) Thread {
	view := m.View()
	target := view.Main
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
