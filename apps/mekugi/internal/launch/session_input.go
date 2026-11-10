package launch

import (
	"context"
	"strings"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	"github.com/yusing/mekugi/modules/conversation/completion"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

func (session *nativeSession) submitInput(attached *attachedPresentation, picker *composerPicker, editor **composer.Editor, clock func() time.Time, side **sessioncontrol.Side, overlay bool, parent context.Context, intent sessionpresentation.SubmitIntent) error {
	defer func() {
		settlePresentation(attached, &session.Client, picker, editor, session.settings, session.serviceTiers, session.title, session.backendVersion, session.restoring != nil, (*side), clock, &session.questions, &session.approvals, overlay)
	}()
	draft, plan, err := attached.presentation.TakeInput(intent, &session.Client.Live, attached.conversation, session.restoring != nil)
	if err != nil {
		setNotice(&attached.feedback, presentationTime(clock), err.Error(), true)
		return nil
	}
	// Attachment preparation must not move an admitted input to a later message.
	var messageBoundary uint64
	if !plan.Queued {
		messageBoundary = session.controlObservations.AssistantMessage(attached.cwd, session.Client.Thread)
	}
	input := prepareDraft(picker, attached.cwd, session.skillEnvironment, draft)
	input.MessageBoundary = messageBoundary
	attached.conversation.QueueInput(input, plan.Queued)
	pruneDraftImages(session.ownedImages, (*editor), attached.conversation, &session.questions, &session.approvals, (*side), attached.hooks.nativeImages)
	setNotice(&attached.feedback, presentationTime(clock), "", false)
	if !plan.Flush {
		return nil
	}
	return session.flushInput(attached, picker, clock, editor, parent)
}

func (session *nativeSession) applyComposerCommand(attached *attachedPresentation, picker *composerPicker, editor **composer.Editor, clock func() time.Time, side **sessioncontrol.Side, panel **appServerStatusReport, resumePicker **appServerResumePicker, parent context.Context, files *detailFiles, intent sessionpresentation.ComposerIntent, preparation *composerPreparation) (bool, error) {
	if err := attached.presentation.AdmitClientComposer(intent); err != nil {
		setNotice(&attached.feedback, presentationTime(clock), err.Error(), true)
		return false, nil
	}
	inputThread := intent.Thread
	if inputThread == "" {
		inputThread = attached.presentation.View().Input.ID
	}
	command := strings.Fields((*editor).Text)
	settingsIntent := intent.Operation == sessionpresentation.IncreaseEffort || intent.Operation == sessionpresentation.DecreaseEffort
	if len(command) > 0 && (intent.Operation == sessionpresentation.SubmitComposer || intent.Operation == sessionpresentation.QueueComposer) {
		settingsIntent = command[0] == "/model" || command[0] == "/effort" || command[0] == "/reasoning" || command[0] == "/tier" || command[0] == "/permissions"
	}
	if settingsIntent && inputThread != attached.presentation.View().Input.ID {
		setNotice(&attached.feedback, presentationTime(clock), "Settings target changed · retry the control", false)
		return false, nil
	}
	if sideThread := attached.presentation.View().SideThread.ID; sideThread != "" && inputThread == sideThread {
		text := strings.TrimSpace((*editor).Text)
		switch intent.Operation {
		case sessionpresentation.SubmitComposer, sessionpresentation.QueueComposer:
			if !strings.HasPrefix(text, "/") && !strings.HasPrefix(text, "!") {
				return false, session.submitBTW(attached, picker, editor, clock, side, session.overlayOpen(*panel, *resumePicker), preparation)
			}
			command := strings.Fields(text)
			setting := len(command) > 0 && (command[0] == "/model" || command[0] == "/effort" || command[0] == "/reasoning")
			if text != "/copy" && !setting {
				setNotice(&attached.feedback, presentationTime(clock), "Return to Main to use this command", false)
				return false, nil
			}
		case sessionpresentation.RestoreWaiting:
			setNotice(&attached.feedback, presentationTime(clock), "Return to Main to use this control", false)
			return false, nil
		}
	}
	switch intent.Operation {
	case sessionpresentation.IncreaseEffort, sessionpresentation.DecreaseEffort:
		return false, stepReasoning(attached, &session.Client, session.settings, &session.serviceTiers, session.panes, session.restoring != nil, clock, session.models, session.modelsLoading, intent.Operation == sessionpresentation.IncreaseEffort)
	case sessionpresentation.RestoreWaiting:
		active, err := attached.presentation.ResolveInterrupt(sessionpresentation.InterruptIntent{Version: intent.Version, Target: *intent.Work, Mode: sessioncontrol.RestoreWaiting}, &session.Client.Live, &attached.session)
		if err != nil {
			setNotice(&attached.feedback, presentationTime(clock), err.Error(), true)
			return false, nil
		}
		if !active {
			setNotice(&attached.feedback, presentationTime(clock), "Original work already ended.", false)
			return false, nil
		}
		session.guardHookCheck.cancel(session.Client.Thread)
		editQueued(attached, &session.Client, picker, &session.questions, &session.approvals, clock)
	case sessionpresentation.SubmitComposer:
		if session.Client.Replacement.Archiving() {
			setNotice(&attached.feedback, presentationTime(clock), "Archiving session…", false)
			return false, nil
		}
		text := strings.TrimSpace((*editor).Text)
		if text == "/presets" {
			openPresets(parent, attached, &session.Client, picker, *editor, session.settings, session.restoring != nil)
			return false, nil
		}
		if text == "/archive" {
			session.showArchivePicker(attached, picker, editor, (*side), clock, session.overlayOpen(*panel, *resumePicker))
			return false, nil
		}
		if handled, err := session.controlsCommand(attached, picker, editor, clock, side, session.overlayOpen(*panel, *resumePicker), text); handled {
			return false, err
		}
		if session.titleCommand(attached, picker, editor, clock, side, session.overlayOpen(*panel, *resumePicker), text) {
			return false, nil
		}
		if sideCommand(text) != "" {
			return false, session.submitBTW(attached, picker, editor, clock, side, session.overlayOpen(*panel, *resumePicker), preparation)
		}
		if attached.conversation.ShellMode() {
			return false, session.submitShell(attached, picker, editor, clock, parent)
		}
		if fields := strings.Fields(text); len(fields) > 1 && (fields[0] == "/fork" || fields[0] == "/rewind") {
			setNotice(&attached.feedback, presentationTime(clock), "Use "+fields[0]+" without arguments", true)
			return false, nil
		}
		if text == "/compact" || text == "/clear" || text == "/fork" || text == "/rewind" {
			return false, session.sessionCommand(attached, picker, editor, clock, parent, session.statusConfig.Provider, text)
		}
		if text == "/resume" || strings.HasPrefix(text, "/resume ") {
			return false, resumeCommand(attached, &session.Client, picker, editor, clock, &session.restoring, resumePicker, session.skillEnvironment, session.approvalMode, text)
		}
		if text == "/ps" {
			session.showRunningProcesses(attached, picker, editor, clock, side, panel, resumePicker, files)
			return false, nil
		}
		if text == "/session" {
			session.showSessionMetrics(attached, picker, editor, clock, side, panel, resumePicker)
			return false, nil
		}
		if text == "/status" {
			return false, session.showStatus(attached, picker, editor, clock, side, panel, resumePicker)
		}
		if text == "/copy" {
			session.showCopyPicker(attached, picker, editor, clock, side, panel, resumePicker)
			return false, nil
		}
		if text == "/skills" {
			session.deleteDraftRange(attached, picker, editor, (*side), clock, session.overlayOpen(*panel, *resumePicker), 0, len((*editor).Text))
			picker.openMenu("menu")
			picker.Choices = []completion.Choice{{Name: "list"}, {Name: "manage"}}
			return false, nil
		}
		if text == "/quit" {
			if session.Client.Turn == "" && !session.Client.Starting() && !session.Client.InputPending() {
				(*editor).Text = ""
				(*editor).Changed()
				return true, nil
			}
			setNotice(&attached.feedback, presentationTime(clock), "Interrupt the active turn before quitting", false)
			return false, nil
		}
		if handled, err := session.settingsCommand(attached, picker, editor, (*side), clock, session.overlayOpen(*panel, *resumePicker), text); handled {
			return false, err
		}
		if strings.HasPrefix(text, "/") {
			setNotice(&attached.feedback, presentationTime(clock), "Unknown command "+strings.Fields(text)[0]+" · Type / to see available commands", true)
			return false, nil
		}
		if text == "" || session.Client.Thread == "" || session.restoring != nil {
			return false, nil
		}
		view := attached.presentation.View()
		return false, session.submitInput(attached, picker, editor, clock, side, session.overlayOpen(*panel, *resumePicker), parent, sessionpresentation.SubmitIntent{Version: view.Version, Revision: view.Draft.Revision, Mode: sessioncontrol.SendInput})
	case sessionpresentation.QueueComposer:
		// Tab queues busy input for the next turn; otherwise it sends like Enter.
		text := strings.TrimSpace((*editor).Text)
		if text == "/compact" {
			return false, session.submitCompact(attached, picker, editor, clock, parent, true)
		}
		if attached.conversation.ShellMode() || text == "" || strings.HasPrefix(text, "/") || !session.Client.PlanInput(sessioncontrol.QueueNextTurn, false).Queued {
			intent.Operation = sessionpresentation.SubmitComposer
			return session.applyComposerCommand(attached, picker, editor, clock, side, panel, resumePicker, parent, files, intent, preparation)
		}
		view := attached.presentation.View()
		return false, session.submitInput(attached, picker, editor, clock, side, session.overlayOpen(*panel, *resumePicker), parent, sessionpresentation.SubmitIntent{Version: view.Version, Revision: view.Draft.Revision, Mode: sessioncontrol.QueueNextTurn})
	}
	return false, nil
}
