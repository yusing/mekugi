package router

import (
	"cmp"
	"time"

	presentationcontract "github.com/yusing/mekugi/contracts/presentation"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
)

// publishPresentation settles detached facts at a semantic transition boundary.
// Reading Model.View does not run this projection or bind an attachment.
func (u *appServerUI) publishPresentation(now time.Time) sessionpresentation.View {
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
			caption.Tier = u.serviceTiers.Effective(caption.Model, caption.Tier, agent.thread)
		}
		caption.ContextKnown, caption.ContextTokens, caption.ContextWindow = agent.ContextKnown, agent.ContextTokens, agent.ContextWindow
		caption.Items = m.StatusItems(presentationcontract.Thread{ID: view.Displayed.ID, OutputThroughput: agent.OutputThroughput}, presentationcontract.ComposerCaption)
	}
	return m.Publish(sessionpresentation.MainInputs{
		Control: &u.Live, Feedback: &u.feedback, Conversation: u.conversation(), Restoring: u.restoring != nil,
		SideRunning: u.side != nil && (u.side.Busy || u.side.Starting), Processes: u.runningProcessCount(), History: u.historyViews(),
	}, caption, now)
}

func (u *appServerUI) sessionPresentation() *sessionpresentation.Model {
	return u.presentationState.Session(u.session.Scope())
}

func (u *appServerUI) attachPresentation() {
	u.bindConversation()
	m := u.sessionPresentation()
	m.Attach(sessionpresentation.Thread{ID: cmp.Or(u.Thread, "/root"), Caller: "/root"}, u.replay != nil)
	if u.agents != nil {
		u.agents.display = m
	}
}

// settlePresentation runs before another intent can observe this session.
// Opening a prompt does not mark it painted or acknowledge a notice.
func (u *appServerUI) settlePresentation() {
	u.autoOpenApprovals()
	u.autoOpenQuestions()
	u.publishPresentation(u.now())
}

func (v *activityAdapter) displayingChild() bool {
	view := v.display.View()
	return view.Displayed.ID != view.Main.ID
}

func (v *activityAdapter) displayedCaller() string { return v.display.View().Displayed.Caller }

// displaySession accepts detached targets without consulting terminal state.
func (u *appServerUI) displaySession(intent sessionpresentation.DisplayIntent) bool {
	defer u.settlePresentation()
	m := u.sessionPresentation()
	if intent.Thread != m.View().Main && u.session.paths[intent.Thread.ID] != intent.Thread.Caller {
		u.setNotice(sessionpresentation.ErrStaleIntent.Error(), true)
		return false
	}
	if err := m.Display(intent); err != nil {
		u.setNotice(err.Error(), true)
		return false
	}
	return true
}

// Roster keys and hit targets resolve to stable thread identities before
// admission. The highlighted row remains renderer-local.
func (v *activityAdapter) displayTarget(caller string) sessionpresentation.Thread {
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
	}
	return target
}
