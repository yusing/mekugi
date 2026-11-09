package launch

import (
	"context"
	"iter"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/modules/conversation/composer"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

// applyInput binds terminal effects. Each effect settles before the terminal
// resumes interpretation; no application key or pointer switch remains here.
func (session *nativeSession) applyInput(attached *attachedPresentation, terminal *terminalsession.Attachment, picker *composerPicker, editor **composer.Editor, clock func() time.Time, side **sessioncontrol.Side, panel **appServerStatusReport, resumePicker **appServerResumePicker, parent context.Context, dirty *bool, files *detailFiles, quit *bool, surface *terminalsession.Attachment, effects iter.Seq[terminalsession.InputEffect]) error {
	if attached != nil {
		publishTestPresentation(attached, terminal, &session.Client, picker, editor, session.settings, session.serviceTiers, session.title, session.backendVersion, session.restoring != nil, (*side))
		publishActivity(attached, terminal)
		defer func() {
			settleTestPresentation(attached, terminal, &session.Client, picker, editor, session.settings, session.serviceTiers, session.title, session.backendVersion, session.restoring != nil, (*side), clock, &session.questions, &session.approvals, session.overlayOpen(*panel, *resumePicker))
		}()
	}
	for effect := range effects {
		more, err := session.applyEffect(attached, terminal, picker, editor, clock, side, panel, resumePicker, parent, dirty, files, quit, surface, effect)
		if err != nil || !more {
			return err
		}
		if attached != nil {
			publishTestPresentation(attached, terminal, &session.Client, picker, editor, session.settings, session.serviceTiers, session.title, session.backendVersion, session.restoring != nil, (*side))
		}
	}
	return nil
}

func (session *nativeSession) applyEffect(attached *attachedPresentation, terminal *terminalsession.Attachment, picker *composerPicker, editor **composer.Editor, clock func() time.Time, side **sessioncontrol.Side, panel **appServerStatusReport, resumePicker **appServerResumePicker, parent context.Context, dirty *bool, files *detailFiles, quit *bool, surface *terminalsession.Attachment, effect terminalsession.InputEffect) (bool, error) {
	switch {
	case effect.Focused != nil:
		if attached != nil && session.notifications != nil {
			session.notifications.focused = *effect.Focused
		}
	case effect.Paste != nil:
		_, err := session.input(attached, terminal, picker, editor, clock, side, panel, resumePicker, parent, dirty, files, *effect.Paste)
		return false, err
	case effect.Main != nil:
		shouldQuit, err := session.applyMainKey(attached, terminal, picker, editor, clock, side, panel, resumePicker, parent, dirty, files, *effect.Main)
		(*quit) = (*quit) || shouldQuit
		return false, err
	case effect.Prompt != nil:
		handled, err := session.applyPromptInput(attached, terminal, picker, editor, clock, side, session.overlayOpen(*panel, *resumePicker), parent, *effect.Prompt)
		return !handled, err
	case effect.PendingPrompt != nil:
		if effect.PendingPrompt.Version != attached.presentation.View().Version {
			setNotice(&attached.feedback, presentationTime(clock), sessionpresentation.ErrStaleIntent.Error(), true)
			return false, nil
		}
		if attached.presentation.DisplayingSide() {
			view := attached.presentation.View()
			_ = attached.presentation.Display(sessionpresentation.DisplayIntent{Version: view.Version, Thread: view.Main})
			*editor = &attached.conversation.Editor
		}
		if !effect.PendingPrompt.ApprovalsFirst || !openApprovals(&session.approvals, &session.questions, attached.conversation, picker, editor, session.Client.Thread) {
			openQuestions(attached, picker, editor, &session.questions, &session.approvals, session.Client.Thread)
		}
	case effect.Panel != nil:
		applyPanelAction(attached, clock, panel, session.sessionCapture, *effect.Panel)
		(*dirty) = true
	case effect.Resume != nil:
		return false, session.applyResumeAction(attached, picker, editor, clock, side, panel, resumePicker, quit, *effect.Resume)
	case effect.Menu != nil:
		handled := session.applyTestMenuResult(attached, terminal, picker, editor, side, clock, session.overlayOpen(*panel, *resumePicker), parent, *effect.Menu)
		session.dispatchRewind(attached, picker, clock, parent)
		return !handled, nil
	case effect.Side != nil:
		return false, applyCloseSide(attached, terminal, &session.Client, picker, editor, &session.questions, &session.approvals, clock, session.settings, session.serviceTiers, session.title, session.backendVersion, session.restoring != nil, side, session.overlayOpen(*panel, *resumePicker), &session.ownedImages, *effect.Side)
	case effect.BTW != nil:
		view := attached.presentation.View()
		if effect.BTW.Version == view.Version && view.BTW != nil && effect.BTW.Exchange == view.BTW.ID {
			return false, closeBTWQuestion(attached, &session.Client)
		}
	case effect.Dialog != nil:
		session.outputIntent(attached, terminal, picker, editor, clock, side, panel, resumePicker, parent, files, surface, *effect.Dialog)
	case effect.Reference != nil:
		return session.referenceInput(attached, picker, editor, clock, side, panel, resumePicker, parent, *effect.Reference), nil
	case effect.Selection != nil:
		result := effect.Selection
		if len(result.Demands) > 0 {
			source := activitySource(attached, terminal, result.Selection.View)
			if source == nil {
				setNotice(&attached.feedback, presentationTime(clock), sessionpresentation.ErrStaleIntent.Error(), true)
				return false, nil
			}
			result.Selection.View.Hydrate(source.ResolveOutputDemands(result.Demands))
		} else {
			openSelectionTarget(attached, terminal, clock, session.reviewSource, files, surface, result.Target)
		}

	case effect.Display != nil:
		displaySession(attached, terminal, &session.Client, picker, editor, session.settings, session.serviceTiers, session.title, session.backendVersion, session.restoring != nil, (*side), clock, &session.questions, &session.approvals, session.overlayOpen(*panel, *resumePicker), *effect.Display)
	case effect.Roster != nil:
		intent := effect.Roster
		target := attached.presentation.ActivityTarget(intent.Caller, attached.childSource)
		if displaySession(attached, terminal, &session.Client, picker, editor, session.settings, session.serviceTiers, session.title, session.backendVersion, session.restoring != nil, (*side), clock, &session.questions, &session.approvals, session.overlayOpen(*panel, *resumePicker), sessionpresentation.DisplayIntent{Version: sessionpresentation.Version{Attachment: intent.Attachment, Revision: intent.Revision}, Thread: target}) && target.Caller != "/root" {
			surface.Agents.Selected = target.Caller
		}
	case effect.Details != nil:
		intent := effect.Details
		if intent.Version != attached.presentation.View().Version || activitySource(attached, terminal, intent.View) == nil {
			setNotice(&attached.feedback, presentationTime(clock), sessionpresentation.ErrStaleIntent.Error(), true)
			return false, nil
		}
		switch intent.Kind {
		case terminalsession.ErrorDetails:
			openErrorDetails(attached, terminal, files, surface, intent.View)
		case terminalsession.SkillDetails:
			openSkills(attached, terminal, files, surface, intent.View, intent.Caller)
		}
	case effect.Notice != nil:
		intent := effect.Notice
		view := attached.presentation.View()
		if intent.Version != view.Version || intent.Feedback != view.Status.FeedbackRevision {
			setNotice(&attached.feedback, presentationTime(clock), sessionpresentation.ErrStaleIntent.Error(), true)
			return false, nil
		}
		if intent.Dismiss {
			setNotice(&attached.feedback, presentationTime(clock), "", false)
			if attached.presentation.Status.Alert {
				attached.presentation.Status.Set("", false)
			}
		} else {
			openBlocks(attached, terminal, files, surface, terminal.Transcript, []activityui.Block{{Kind: "error", Body: intent.Text}})
		}
		(*dirty) = true
	case effect.Clipboard != nil:
		setNotice(&attached.feedback, presentationTime(clock), effect.Clipboard.Text, effect.Clipboard.Alert)
	case effect.Interrupt != nil:
		return false, session.interruptSession(attached, picker, editor, clock, side, session.overlayOpen(*panel, *resumePicker), *effect.Interrupt)
	case effect.Locked:
		lockNotice(attached, clock)
	case effect.EditorPath != "":
		return false, &openFileEditor{path: effect.EditorPath}
	}
	return true, nil
}
