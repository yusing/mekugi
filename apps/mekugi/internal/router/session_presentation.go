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
	if agent := u.session.Agent(view.Displayed.Caller); agent != nil {
		if view.Displayed.ID != view.Main.ID {
			caption.Model, caption.Effort = agent.Settings.Model, agent.Settings.Effort
			if agent.Settings.TierKnown {
				caption.Tier = cmp.Or(agent.Settings.Tier, "default")
			}
			caption.Tier = u.serviceTiers.Effective(caption.Model, caption.Tier, agent.Thread)
		}
		caption.ContextKnown, caption.ContextTokens, caption.ContextWindow = agent.ContextKnown, agent.ContextTokens, agent.ContextWindow
		caption.Items = m.StatusItems(presentationcontract.Thread{ID: view.Displayed.ID, OutputThroughput: agent.OutputThroughput}, presentationcontract.ComposerCaption)
	}
	view = m.Publish(sessionpresentation.MainInputs{
		Resume: u.resumeView(), Panel: u.panelView(), Control: &u.Live, Feedback: &u.feedback, Conversation: u.conversation(), Restoring: u.restoring != nil,
		SideRunning: u.side != nil && (u.side.Busy || u.side.Starting), Processes: u.runningProcessCount(), History: u.historyViews(),
	}, caption, now)
	u.prompts.Update(view)
	u.mainView.Panel.Update(view)
	u.mainView.Resume.Update(view)
	u.mainView.Side.Update(view)
	return view
}

func (u *appServerUI) sessionPresentation() *sessionpresentation.Model {
	return u.presentationState.Session(u.session.Scope())
}

func (u *appServerUI) attachPresentation() {
	u.attachReview()
	u.attachActivity()
	u.bindConversation()
	m := u.sessionPresentation()
	m.Attach(sessionpresentation.Thread{ID: cmp.Or(u.Thread, "/root"), Caller: "/root"}, u.replay != nil)
}

// settlePresentation runs before another intent can observe this session.
// Opening a prompt does not mark it painted or acknowledge a notice.
func (u *appServerUI) settlePresentation() {
	u.autoOpenApprovals()
	u.autoOpenQuestions()
	u.publishPresentation(u.now())
	u.publishActivity()
}

// displaySession accepts detached targets without consulting terminal state.
func (u *appServerUI) displaySession(intent sessionpresentation.DisplayIntent) bool {
	defer u.settlePresentation()
	m := u.sessionPresentation()
	if intent.Thread != m.View().Main && u.session.KnownCaller(intent.Thread.ID) != intent.Thread.Caller {
		u.setNotice(sessionpresentation.ErrStaleIntent.Error(), true)
		return false
	}
	if err := m.Display(intent); err != nil {
		u.setNotice(err.Error(), true)
		return false
	}
	return true
}
