package launch

import (
	"cmp"
	"context"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/contracts/observation"
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
func publishPresentation(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, settings preferences.Applied, tiers *inferencegateway.Tiers, title string, backend string, restoring bool, side *sessioncontrol.Side, now time.Time) sessionpresentation.View {
	m := attached.presentation
	if attached.view != "" {
		view := publishSessionView(attached, control, picker, "", settings, tiers, title, backend, restoring, side, now, true)
		terminal.Main.Update(view)
		return view
	}
	if side != nil {
		id := cmp.Or(side.Thread, "side:"+side.Key)
		m.SetSide(sessionpresentation.Thread{ID: id, Caller: side.Caller()})
		if side.Thread != "" {
			attached.session.RegisterThread(observation.Thread{ID: side.Thread, Path: side.Caller()})
			agent := attached.session.Activity().Agent(side.Thread)
			agent.Isolated = true
			if !agent.SettingsLive {
				agent.Settings = preferences.Evidence{Model: side.Model, Effort: side.Effort, Tier: side.Tier, TierKnown: side.Model != "" || side.Tier != ""}
			}
		}
	} else {
		m.SetSide(sessionpresentation.Thread{})
	}
	if m.DisplayingSide() {
		*editor = &side.Drafts.Editor
	} else if q := attached.conversation.Questions.Current(); q != nil && q.IsSecret {
		*editor = &terminal.Prompts.Secret
	} else {
		*editor = &attached.conversation.Editor
	}
	view := publishSessionView(attached, control, picker, (*editor).Text, settings, tiers, title, backend, restoring, side, now, false)
	terminal.Prompts.Update(view)
	terminal.Main.Update(view)
	for _, terminal := range []*activityui.View{terminal.Transcript, terminal.Agents} {
		if terminal != nil {
			terminal.Painter.FileLink = markdownFileLinks(view.Info.Workspace)
			terminal.Painter.DeferFileLinks = true
		}
	}
	return view
}

// publishSessionView projects semantic owner state without choosing an editor,
// touching renderer geometry or acknowledging terminal delivery.
func publishSessionView(attached *attachedPresentation, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, draft string, settings preferences.Applied, tiers *inferencegateway.Tiers, title, backend string, restoring bool, side *sessioncontrol.Side, now time.Time, clientEditors bool) sessionpresentation.View {
	m := attached.presentation
	if control.Startup.Prepared != nil {
		m.RestrictChildInput()
	}
	if side != nil {
		m.SetSide(sessionpresentation.Thread{ID: cmp.Or(side.Thread, "side:"+side.Key), Caller: side.Caller()})
	} else {
		m.SetSide(sessionpresentation.Thread{})
	}
	view := m.View()
	var caption sessionpresentation.Caption
	if view.Displayed.ID == view.Main.ID {
		caption.Model, caption.Effort, caption.Tier = settings.Model, settings.Effort, tiers.Effective(settings.Model, settings.Tier, control.Thread)
	}
	if m.DisplayingSide() {
		caption = sessionpresentation.Caption{Model: side.Model, Effort: side.Effort, Tier: tiers.Effective(side.Model, side.Tier, side.Thread)}
	}
	caption = sessionCaption(&attached.session, m, caption, tiers)
	info := sessionpresentation.MainInfo{Workspace: attached.cwd, Title: cmp.Or(control.Names.Pending(control.Thread), title), BackendVersion: backend, Skills: attached.source.ActiveSkills()["Main"]}
	if approval := attached.conversation.Approvals.View(); approval.Count > 0 {
		if caller := attached.session.KnownCaller(approval.Approval.Thread); caller != "/root" {
			info.ApprovalCaller = caller
		}
	}
	return m.Publish(sessionpresentation.MainInputs{
		BTWRunning: control.BTW != nil && (control.BTW.Busy || control.BTW.Starting),
		Info:       info, ClientEditors: clientEditors,
		Menu: picker.menuView(attached.cwd, draft, &control.Live), Control: &control.Live, Feedback: &attached.feedback, Conversation: attached.conversation, Restoring: restoring,
		SideThreads: control.Requests.SideThreads(side), SideRunning: side != nil && (side.Busy || side.Starting), Processes: runningProcessCount(&attached.session),
	}, caption, now)
}

func attachPresentation(attached *attachedPresentation, terminal *terminalsession.Attachment, picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, main string, review **sessionreview.Review, commits **activitypresentation.CommitReader, parent context.Context, retained changes.Scope, changeSource changes.Service, activityPresent bool) {
	(*review) = attachReview(parent, (*review), retained, changeSource)
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
func settlePresentation(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, settings preferences.Applied, tiers *inferencegateway.Tiers, title string, backend string, restoring bool, side *sessioncontrol.Side, clock func() time.Time, questions *nativeQuestionDock, approvals *nativeApprovalDock, overlay bool) {
	if picker.Open && picker.Kind == "rewind-edits" && !picker.Loading && picker.rewind.problem == "" && picker.rewind.command == "" && !restoring && !control.Replacement.Pending() && control.Thread == picker.rewindThread && !sessionBusy(&control.Live, attached.conversation) {
		forkChosenRewind(attached, terminal, control, picker, settings, clock)
	}
	if attached.view == "" {
		autoOpenApprovals(attached, terminal, picker, editor, questions, approvals, control.Thread, overlay)
		autoOpenQuestions(attached, terminal, picker, editor, questions, approvals, control.Thread, overlay)
	}
	publishPresentation(attached, terminal, control, picker, editor, settings, tiers, title, backend, restoring, side, presentationTime(clock))
	publishActivity(attached, terminal)
}

// sessionCaption borrows the selected source's facts; it retains no accounting state.
func sessionCaption(session *sessionpresentation.Attachment, m *sessionpresentation.Model, caption sessionpresentation.Caption, tiers *inferencegateway.Tiers) sessionpresentation.Caption {
	view := m.View()
	if agent := session.Agent(view.Displayed.Caller); agent != nil {
		if view.Displayed.ID != view.Main.ID {
			caption.Model, caption.Effort = agent.Settings.Model, agent.Settings.Effort
			caption.Role = agent.Role
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
