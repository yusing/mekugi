package router

import (
	"cmp"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/contracts/scope"
)

func (s *appServerSession) threadState(thread string) *scope.Store {
	if s.threadStates == nil {
		s.threadStates = make(map[string]*scope.Store)
	}
	if s.threadStates[thread] == nil {
		s.threadStates[thread] = new(scope.Store)
	}
	return s.threadStates[thread]
}

func (u *appServerUI) sessionPresentation() *sessionpresentation.Model {
	m := u.presentationState.Session(&u.session.state)
	m.Attach(sessionpresentation.Thread{ID: cmp.Or(u.Thread, "/root"), Caller: "/root"}, u.replay != nil)
	if u.agents != nil {
		u.agents.display = m
	}
	return m
}

func (v *liveActivityView) displayingChild() bool {
	view := v.display.View()
	return view.Displayed.ID != view.Main.ID
}

func (v *liveActivityView) displayedCaller() string { return v.display.View().Displayed.Caller }

// Roster keys and hit targets become a semantic identity before changing the
// displayed transcript. The highlighted row remains renderer-local.
func (v *liveActivityView) displayThread(caller string) {
	view := v.display.View()
	target := view.Main
	if caller != "/root" && caller != "" {
		target = sessionpresentation.Thread{ID: caller, Caller: caller}
		for _, row := range v.Roster() {
			if row.Agent.Name == caller {
				target.ID = cmp.Or(row.Agent.thread, caller)
				break
			}
		}
		v.selected = caller
	}
	// This synchronous terminal action uses the view it just read. Future
	// detached consumers submit their retained version directly to the owner.
	_ = v.display.Display(sessionpresentation.DisplayIntent{Version: view.Version, Thread: target})
}
