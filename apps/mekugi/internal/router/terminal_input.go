package router

import (
	"iter"

	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

// applyInput binds terminal effects. Each effect settles before the terminal
// resumes interpretation; no application key or pointer switch remains here.
func (main *appServerUI) applyInput(u *terminalsession.Attachment, effects iter.Seq[terminalsession.InputEffect]) error {
	if main != nil {
		publishPresentation(&main.attached, main.shell, &main.Client, &main.picker, &main.Editor, &main.childHistoryState, preferences.Applied{Model: main.model, Effort: main.reasoningEffort, Tier: main.serviceTier}, main.serviceTiers, main.title, main.backendVersion, main.restoring != nil, main.side, presentationTime(main.clock))
		publishActivity(&main.attached, main.shell)
		defer func() {
			settlePresentation(&main.attached, main.shell, &main.Client, &main.picker, &main.Editor, &main.childHistoryState, preferences.Applied{Model: main.model, Effort: main.reasoningEffort, Tier: main.serviceTier}, main.serviceTiers, main.title, main.backendVersion, main.restoring != nil, main.side, main.clock, &main.questions, &main.approvals, main.statusPanel != nil || main.resumePicker != nil)
		}()
	}
	for effect := range effects {
		more, err := main.applyEffect(u, effect)
		if err != nil || !more {
			return err
		}
		if main != nil {
			publishPresentation(&main.attached, main.shell, &main.Client, &main.picker, &main.Editor, &main.childHistoryState, preferences.Applied{Model: main.model, Effort: main.reasoningEffort, Tier: main.serviceTier}, main.serviceTiers, main.title, main.backendVersion, main.restoring != nil, main.side, presentationTime(main.clock))
		}
	}
	return nil
}

func (main *appServerUI) applyEffect(u *terminalsession.Attachment, effect terminalsession.InputEffect) (bool, error) {
	m := main
	switch {
	case effect.Focused != nil:
		if m != nil && m.notifications != nil {
			m.notifications.focused = *effect.Focused
		}
	case effect.Paste != nil:
		_, err := m.input(*effect.Paste)
		return false, err
	case effect.Main != nil:
		quit, err := m.applyMainKey(*effect.Main)
		m.quitRequested = m.quitRequested || quit
		return false, err
	case effect.Prompt != nil:
		handled, err := m.applyPromptInput(*effect.Prompt)
		return !handled, err
	case effect.PendingPrompt != nil:
		if effect.PendingPrompt.Version != m.attached.presentation.View().Version {
			setNotice(&m.attached.feedback, m.shell, presentationTime(m.clock), sessionpresentation.ErrStaleIntent.Error(), true)
			return false, nil
		}
		if !effect.PendingPrompt.ApprovalsFirst || !openApprovals(&m.approvals, &m.questions, m.attached.conversation, m.shell, &m.picker, &m.Editor, m.Thread) {
			openQuestions(&m.attached, m.shell, &m.picker, &m.Editor, &m.questions, &m.approvals, m.Thread)
		}
	case effect.Panel != nil:
		m.applyPanelAction(*effect.Panel)
		m.dirty = true
	case effect.Resume != nil:
		return false, m.applyResumeAction(*effect.Resume)
	case effect.Menu != nil:
		return !m.applyMenuResult(*effect.Menu), nil
	case effect.Side != nil:
		return false, m.applyCloseSide(*effect.Side)
	case effect.Dialog != nil:
		main.outputIntent(u, *effect.Dialog)
	case effect.Reference != nil:
		return m.referenceInput(*effect.Reference), nil
	case effect.Selection != nil:
		result := effect.Selection
		if len(result.Demands) > 0 {
			source := activitySource(&m.attached, m.shell, result.Selection.View)
			if source == nil {
				setNotice(&m.attached.feedback, m.shell, presentationTime(m.clock), sessionpresentation.ErrStaleIntent.Error(), true)
				return false, nil
			}
			result.Selection.View.Hydrate(source.ResolveOutputDemands(result.Demands))
		} else {
			main.openSelectionTarget(u, result.Target)
		}
	case effect.History != nil:
		if effect.CancelHistory {
			return false, m.cancelOlderActivity(*effect.History)
		}
		return false, m.loadOlderActivity(*effect.History)
	case effect.Display != nil:
		displaySession(&m.attached, m.shell, &m.Client, &m.picker, &m.Editor, &m.childHistoryState, preferences.Applied{Model: m.model, Effort: m.reasoningEffort, Tier: m.serviceTier}, m.serviceTiers, m.title, m.backendVersion, m.restoring != nil, m.side, m.clock, &m.questions, &m.approvals, m.statusPanel != nil || m.resumePicker != nil, *effect.Display)
	case effect.Roster != nil:
		intent := effect.Roster
		target := m.attached.presentation.ActivityTarget(intent.Caller, m.attached.childSource)
		if displaySession(&m.attached, m.shell, &m.Client, &m.picker, &m.Editor, &m.childHistoryState, preferences.Applied{Model: m.model, Effort: m.reasoningEffort, Tier: m.serviceTier}, m.serviceTiers, m.title, m.backendVersion, m.restoring != nil, m.side, m.clock, &m.questions, &m.approvals, m.statusPanel != nil || m.resumePicker != nil, sessionpresentation.DisplayIntent{Version: sessionpresentation.Version{Attachment: intent.Attachment, Revision: intent.Revision}, Thread: target}) && target.Caller != "/root" {
			u.Agents.Selected = target.Caller
		}
	case effect.Details != nil:
		intent := effect.Details
		if intent.Version != m.attached.presentation.View().Version || activitySource(&m.attached, m.shell, intent.View) == nil {
			setNotice(&m.attached.feedback, m.shell, presentationTime(m.clock), sessionpresentation.ErrStaleIntent.Error(), true)
			return false, nil
		}
		switch intent.Kind {
		case terminalsession.ErrorDetails:
			main.openErrorDetails(u, intent.View)
		case terminalsession.SkillDetails:
			main.openSkills(u, intent.View, intent.Caller)
		}
	case effect.Notice != nil:
		intent := effect.Notice
		view := m.attached.presentation.View()
		if intent.Version != view.Version || intent.Feedback != view.Status.FeedbackRevision {
			setNotice(&m.attached.feedback, m.shell, presentationTime(m.clock), sessionpresentation.ErrStaleIntent.Error(), true)
			return false, nil
		}
		if intent.Dismiss {
			setNotice(&m.attached.feedback, m.shell, presentationTime(m.clock), "", false)
			if m.attached.presentation.Status.Alert {
				m.attached.presentation.Status.Set("", false)
			}
		} else {
			openBlocks(&main.attached, main.shell, &main.detailFiles, u, m.shell.Transcript, []activityui.Block{{Kind: "error", Body: intent.Text}})
		}
		m.dirty = true
	case effect.Clipboard != nil:
		setNotice(&m.attached.feedback, m.shell, presentationTime(m.clock), effect.Clipboard.Text, effect.Clipboard.Alert)
	case effect.Interrupt != nil:
		return false, m.interruptSession(*effect.Interrupt)
	case effect.Locked:
		m.lockNotice()
	case effect.EditorPath != "":
		return false, &openFileEditor{path: effect.EditorPath}
	}
	return true, nil
}
