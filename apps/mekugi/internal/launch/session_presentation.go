package launch

import (
	"cmp"
	"context"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/contracts/scope"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	"github.com/yusing/mekugi/modules/conversation/composer"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"

	presentationcontract "github.com/yusing/mekugi/contracts/presentation"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

// publishPresentation settles detached facts at a semantic transition boundary.
// Reading Model.View does not run this projection or bind an attachment.
func publishPresentation(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, childKey *scope.Key[*appServerChildHistory], settings preferences.Applied, tiers *inferencegateway.Tiers, title string, backend string, restoring bool, side *sessioncontrol.Side, now time.Time) sessionpresentation.View {
	m := attached.presentation
	view := m.View()
	var caption sessionpresentation.Caption
	if view.Displayed.ID == view.Main.ID {
		caption.Model, caption.Effort, caption.Tier = settings.Model, settings.Effort, tiers.Effective(settings.Model, settings.Tier, control.Thread)
	}
	caption = sessionCaption(&attached.session, m, caption, tiers)
	info := sessionpresentation.MainInfo{Workspace: attached.cwd, Title: cmp.Or(control.Names.Pending(control.Thread), title), BackendVersion: backend, Skills: attached.source.ActiveSkills()["Main"]}
	if approval := attached.conversation.Approvals.View(); approval.Count > 0 {
		if caller := attached.session.KnownCaller(approval.Approval.Thread); caller != "/root" {
			info.ApprovalCaller = caller
		}
	}
	view = m.Publish(sessionpresentation.MainInputs{
		Info: info,
		Menu: picker.menuView(attached.cwd, (*editor).Text, &control.Live), Control: &control.Live, Feedback: &attached.feedback, Conversation: attached.conversation, Restoring: restoring,
		SideRunning: side != nil && (side.Busy || side.Starting), Processes: runningProcessCount(&attached.session), History: historyViews(attached, childKey),
	}, caption, now)
	terminal.Prompts.Update(view)
	terminal.Main.Update(view)
	for _, terminal := range []*activityui.View{terminal.Transcript, terminal.Agents} {
		if terminal != nil {
			terminal.Painter.FileLink = markdownFileLinks(view.Info.Workspace)
		}
	}
	return view
}

func attachPresentation(attached *attachedPresentation, terminal *terminalsession.Attachment, picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, main string, review **sessionreview.Review, commits **activitypresentation.CommitReader, parent context.Context, retained changes.Scope, changeSource changes.Service, activityPresent bool) {
	(*review) = attachReview(parent, (*review), retained, changeSource)
	attached.bindSources()
	(*commits) = attachCommitReader(&attached.session, (*commits), parent, activityPresent)
	conversation := attached.conversation
	if (*editor) != &terminal.Prompts.Secret {
		(*editor) = &conversation.Editor
	}
	picker.State, picker.MenuState = &conversation.Completion, attached.presentation.Menu()
	questions.Questions, approvals.Approvals = &conversation.Questions, &conversation.Approvals
	m := attached.presentation
	m.Attach(sessionpresentation.Thread{ID: cmp.Or(main, "/root"), Caller: "/root"}, false)
}

// settlePresentation runs before another intent can observe this session.
// Opening a prompt does not mark it painted or acknowledge a notice.
func settlePresentation(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, childKey *scope.Key[*appServerChildHistory], settings preferences.Applied, tiers *inferencegateway.Tiers, title string, backend string, restoring bool, side *sessioncontrol.Side, clock func() time.Time, questions *nativeQuestionDock, approvals *nativeApprovalDock, overlay bool) {
	autoOpenApprovals(attached, terminal, picker, editor, questions, approvals, control.Thread, overlay)
	autoOpenQuestions(attached, terminal, picker, editor, questions, approvals, control.Thread, overlay)
	publishPresentation(attached, terminal, control, picker, editor, childKey, settings, tiers, title, backend, restoring, side, presentationTime(clock))
	publishActivity(attached, terminal)
}

// displaySession accepts detached targets without consulting terminal state.
func displaySession(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, childKey *scope.Key[*appServerChildHistory], settings preferences.Applied, tiers *inferencegateway.Tiers, title string, backend string, restoring bool, side *sessioncontrol.Side, clock func() time.Time, questions *nativeQuestionDock, approvals *nativeApprovalDock, overlay bool, intent sessionpresentation.DisplayIntent) bool {
	defer func() {
		settlePresentation(attached, terminal, control, picker, editor, childKey, settings, tiers, title, backend, restoring, side, clock, questions, approvals, overlay)
	}()
	m := attached.presentation
	if intent.Thread != m.View().Main && attached.session.KnownCaller(intent.Thread.ID) != intent.Thread.Caller {
		setNotice(&attached.feedback, terminal, presentationTime(clock), sessionpresentation.ErrStaleIntent.Error(), true)
		return false
	}
	if err := m.Display(intent); err != nil {
		setNotice(&attached.feedback, terminal, presentationTime(clock), err.Error(), true)
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
