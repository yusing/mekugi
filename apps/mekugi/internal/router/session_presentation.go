package router

import (
	"cmp"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/contracts/scope"
)

// mainPresentation adapts current owner facts once for any consumer. Caption
// lookup uses the displayed identity, never reverse-searches a display name.
func (u *appServerUI) mainPresentation(now time.Time) sessionpresentation.View {
	m := u.sessionPresentation()
	view := m.View()
	var caption sessionpresentation.Caption
	if view.Displayed.ID == view.Main.ID {
		caption.Model, caption.Effort, caption.Tier = u.model, u.reasoningEffort, u.displayServiceTier()
	}
	if agent := u.session.agent(view.Displayed.Caller); agent != nil {
		if view.Displayed.ID != view.Main.ID {
			caption.Model, caption.Effort = agent.settings.Model, agent.settings.Effort
			if agent.settings.TierKnown {
				caption.Tier = cmp.Or(agent.settings.Tier, "default")
			}
			caption.Tier = effectiveServiceTier(caption.Model, caption.Tier, u.serviceTiers, agent.thread)
		}
		caption.ContextKnown, caption.ContextTokens, caption.ContextWindow = agent.ContextKnown, agent.ContextTokens, agent.ContextWindow
		caption.OutputThroughput = agent.OutputThroughput
	}
	return m.Publish(sessionpresentation.MainInputs{
		Control: &u.Live, Feedback: &u.feedback, Questions: &u.questions.Questions, Approvals: &u.approvals.Approvals,
		DraftKept: u.promptEditor.Parked.Snapshot.Text != "", ShellMode: u.shellMode(), Restoring: u.restoring != nil,
		SideRunning: u.btw != nil && (u.btw.Busy || u.btw.Starting), Processes: u.runningProcessCount(),
	}, caption, now)
}

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

func (v *activityAdapter) displayingChild() bool {
	view := v.display.View()
	return view.Displayed.ID != view.Main.ID
}

func (v *activityAdapter) displayedCaller() string { return v.display.View().Displayed.Caller }

// Roster keys and hit targets become a semantic identity before changing the
// displayed transcript. The highlighted row remains renderer-local.
func (v *activityAdapter) displayThread(caller string) {
	view := v.display.View()
	target := view.Main
	if caller != "/root" && caller != "" {
		target = sessionpresentation.Thread{ID: caller, Caller: caller}
		for _, row := range v.Model.Roster() {
			if row.Agent.Name == caller {
				target.ID = cmp.Or(row.Agent.thread, caller)
				break
			}
		}
		v.Selected = caller
	}
	// This synchronous terminal action uses the view it just read. Future
	// detached consumers submit their retained version directly to the owner.
	_ = v.display.Display(sessionpresentation.DisplayIntent{Version: view.Version, Thread: target})
	v.publishActivity()
}
