package router

import (
	"cmp"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"

	presentationcontract "github.com/yusing/mekugi/contracts/presentation"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

// publishPresentation settles detached facts at a semantic transition boundary.
// Reading Model.View does not run this projection or bind an attachment.
func (u *appServerUI) publishPresentation(now time.Time) sessionpresentation.View {
	m := u.attached.presentation
	view := m.View()
	var caption sessionpresentation.Caption
	if view.Displayed.ID == view.Main.ID {
		caption.Model, caption.Effort, caption.Tier = u.model, u.reasoningEffort, u.serviceTiers.Effective(u.model, u.serviceTier, u.Thread)
	}
	caption = sessionCaption(&u.attached.session, m, caption, u.serviceTiers)
	info := sessionpresentation.MainInfo{Workspace: u.attached.cwd, Title: cmp.Or(u.Names.Pending(u.Thread), u.title), BackendVersion: u.backendVersion, Skills: u.attached.source.ActiveSkills()["Main"]}
	if approval := u.approvals.View(); approval.Count > 0 {
		if caller := u.attached.session.KnownCaller(approval.Approval.Thread); caller != "/root" {
			info.ApprovalCaller = caller
		}
	}
	view = m.Publish(sessionpresentation.MainInputs{
		Info: info,
		Menu: u.picker.menuView(u.attached.cwd, u.Text, &u.Live), Control: &u.Live, Feedback: &u.attached.feedback, Conversation: u.attached.conversation, Restoring: u.restoring != nil,
		SideRunning: u.side != nil && (u.side.Busy || u.side.Starting), Processes: runningProcessCount(&u.attached.session), History: historyViews(&u.attached, &u.childHistoryState),
	}, caption, now)
	u.shell.Prompts.Update(view)
	u.shell.Main.Update(view)
	for _, terminal := range []*activityui.View{u.shell.Transcript, u.shell.Agents} {
		if terminal != nil {
			terminal.Painter.FileLink = markdownFileLinks(view.Info.Workspace)
		}
	}
	return view
}

func (u *appServerUI) attachPresentation() {
	u.review = attachReview(u.ctx, u.review, u.retained, u.changeSource)
	u.attached.bindSources()
	u.commits = attachCommitReader(&u.attached.session, u.commits, u.ctx, u.activity != nil)
	conversation := u.attached.conversation
	if u.Editor != &u.shell.Prompts.Secret {
		u.Editor = &conversation.Editor
	}
	u.picker.State, u.picker.MenuState = &conversation.Completion, u.attached.presentation.Menu()
	u.questions.Questions, u.approvals.Approvals = &conversation.Questions, &conversation.Approvals
	m := u.attached.presentation
	m.Attach(sessionpresentation.Thread{ID: cmp.Or(u.Thread, "/root"), Caller: "/root"}, false)
}

// settlePresentation runs before another intent can observe this session.
// Opening a prompt does not mark it painted or acknowledge a notice.
func (u *appServerUI) settlePresentation() {
	u.autoOpenApprovals()
	u.autoOpenQuestions()
	u.publishPresentation(presentationTime(u.clock))
	publishActivity(&u.attached, u.shell)
}

// displaySession accepts detached targets without consulting terminal state.
func (u *appServerUI) displaySession(intent sessionpresentation.DisplayIntent) bool {
	defer u.settlePresentation()
	m := u.attached.presentation
	if intent.Thread != m.View().Main && u.attached.session.KnownCaller(intent.Thread.ID) != intent.Thread.Caller {
		setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), sessionpresentation.ErrStaleIntent.Error(), true)
		return false
	}
	if err := m.Display(intent); err != nil {
		setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), err.Error(), true)
		return false
	}
	return true
}

// sessionCaption borrows the selected source's facts; it retains no accounting state.
func sessionCaption(session *sessionpresentation.Attachment, m *sessionpresentation.Model, caption sessionpresentation.Caption, tiers *inferencegateway.Tiers) sessionpresentation.Caption {
	view := m.View()
	if agent := session.Agent(view.Displayed.Caller); agent != nil {
		if view.Displayed.ID != view.Main.ID {
			caption.Model, caption.Effort = agent.Settings.Model, agent.Settings.Effort
			if agent.Settings.TierKnown {
				caption.Tier = cmp.Or(agent.Settings.Tier, "default")
			}
			caption.Tier = tiers.Effective(caption.Model, caption.Tier, agent.Thread)
		}
		caption.ContextKnown, caption.ContextTokens, caption.ContextWindow = agent.ContextKnown, agent.ContextTokens, agent.ContextWindow
		caption.Items = m.StatusItems(presentationcontract.Thread{ID: view.Displayed.ID, OutputThroughput: agent.OutputThroughput}, presentationcontract.ComposerCaption)
	}
	return caption
}
