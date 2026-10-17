package router

import (
	"iter"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

// applyInput binds terminal effects. Each effect settles before the terminal
// resumes interpretation; no application key or pointer switch remains here.
func (u *terminalUI) applyInput(effects iter.Seq[terminalsession.InputEffect]) error {
	if u.main != nil {
		u.main.publishPresentation(u.main.now())
		u.main.publishActivity()
		defer u.main.settlePresentation()
	}
	for effect := range effects {
		more, err := u.applyEffect(effect)
		if err != nil || !more {
			return err
		}
		if u.main != nil {
			u.main.publishPresentation(u.main.now())
		}
	}
	return nil
}

func (u *terminalUI) applyEffect(effect terminalsession.InputEffect) (bool, error) {
	m := u.main
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
		if effect.PendingPrompt.Version != m.sessionPresentation().View().Version {
			m.setNotice(sessionpresentation.ErrStaleIntent.Error(), true)
			return false, nil
		}
		if !effect.PendingPrompt.ApprovalsFirst || !m.openApprovals() {
			m.openQuestions()
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
		u.outputIntent(*effect.Dialog)
	case effect.Reference != nil:
		return m.referenceInput(*effect.Reference), nil
	case effect.Selection != nil:
		result := effect.Selection
		if len(result.Demands) > 0 {
			source := m.activitySource(result.Selection.View)
			if source == nil {
				m.setNotice(sessionpresentation.ErrStaleIntent.Error(), true)
				return false, nil
			}
			result.Selection.View.Hydrate(source.ResolveOutputDemands(result.Demands))
		} else {
			u.openSelectionTarget(result.Target)
		}
	case effect.History != nil:
		if effect.CancelHistory {
			return false, m.cancelOlderActivity(*effect.History)
		}
		return false, m.loadOlderActivity(*effect.History)
	case effect.Display != nil:
		m.displaySession(*effect.Display)
	case effect.Roster != nil:
		intent := effect.Roster
		target := m.sessionPresentation().ActivityTarget(intent.Caller, m.childSource)
		if m.displaySession(sessionpresentation.DisplayIntent{Version: sessionpresentation.Version{Attachment: intent.Attachment, Revision: intent.Revision}, Thread: target}) && target.Caller != "/root" {
			u.agents.Selected = target.Caller
		}
	case effect.Details != nil:
		intent := effect.Details
		if intent.Version != m.sessionPresentation().View().Version || m.activitySource(intent.View) == nil {
			m.setNotice(sessionpresentation.ErrStaleIntent.Error(), true)
			return false, nil
		}
		switch intent.Kind {
		case terminalsession.ErrorDetails:
			u.openErrors()
		case terminalsession.SkillDetails:
			u.openSkills(intent.View, intent.Caller)
		}
	case effect.Notice != nil:
		intent := effect.Notice
		view := m.sessionPresentation().View()
		if intent.Version != view.Version || intent.Feedback != view.Status.FeedbackRevision {
			m.setNotice(sessionpresentation.ErrStaleIntent.Error(), true)
			return false, nil
		}
		if intent.Dismiss {
			m.setNotice("", false)
			if m.sessionPresentation().Status.Alert {
				m.sessionPresentation().Status.Set("", false)
			}
		} else {
			u.openBlocks(m.view, []activityui.Block{{Kind: "error", Body: intent.Text}})
		}
		m.dirty = true
	case effect.Clipboard != nil:
		m.setNotice(effect.Clipboard.Text, effect.Clipboard.Alert)
	case effect.Interrupt != nil:
		return false, m.interruptSession(*effect.Interrupt)
	case effect.Locked:
		m.lockNotice()
	case effect.EditorPath != "":
		return false, &openFileEditor{path: effect.EditorPath}
	}
	return true, nil
}
