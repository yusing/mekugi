package launch

import (
	"context"
	_ "image/gif"
	_ "image/jpeg"
	"iter"
	"time"

	"github.com/yusing/mekugi/contracts/scope"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/backends/codex/binding"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
	"github.com/yusing/mekugi/modules/conversation/composer"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
	"github.com/yusing/mekugi/modules/conversation/prompt"
	conversationui "github.com/yusing/mekugi/modules/conversation/terminalview"
	noticepresentation "github.com/yusing/mekugi/modules/notifications/presentation"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
)

func (u *appServerUI) message(m appserver.Message) (err error) {
	attachment := u.attached.presentation.View().Version.Attachment
	var preferenceOwner *preferences.Attachment
	if u.panes != nil {
		preferenceOwner = u.panes.attachment
	}
	defer func() {
		if attachment != u.attached.presentation.View().Version.Attachment {
			u.terminalViews.ReplaceConversation(u.shell)
			u.detailFiles.closeTerminalOutput(u.shell)
		}
		if u.panes != nil && u.panes.attachment != nil && preferenceOwner != u.panes.attachment {
			if layout, readErr := terminalsession.ReadPreferences(u.panes.attachment.Snapshot().Presentation); readErr == nil {
				u.shell.RestorePreferences(layout, u.shell.Main)
				u.attached.interruptLocked, u.attached.predictionsDisabled = layout.InterruptLocked, layout.PredictionsDisabled
				if stageErr := u.panes.stage(u.shell.Preferences(u.shell.Main)); err == nil {
					err = stageErr
				}
			}
		}
		if u.preparePane != nil {
			u.preparePane(u.attached.session.Scope())
		}
		renderTestPresentation(&u.attached, u.shell, u.attached.presentation.View(), u.Editor)
		if u.resumePicker != nil && err == nil {
			err = u.fillResumePicker()
		}
		if u.review != nil && u.review.Preview() != nil {
			previews := u.review.Preview().Current()
			ids := make([]string, 0, len(previews))
			for _, preview := range previews {
				u.shell.Live.Observe(preview, false, presentationTime(u.clock))
				ids = append(ids, preview.ID)
			}
			u.shell.Live.Resync(u.review.Scope(), ids)
		}
	}()
	return u.nativeSession.dispatchHostMessage(&u.attached, &u.picker, &u.Editor, u.clock, &u.side, &u.statusPanel, &u.resumePicker, u.ctx, m)
}

func (u *appServerUI) applyMainKey(next terminalsession.KeyContinuation) (bool, error) {
	return u.nativeSession.applyMainKey(&u.attached, u.shell, &u.picker, &u.Editor, u.clock, &u.side, &u.statusPanel, &u.resumePicker, u.ctx, &u.dirty, &u.detailFiles, next)
}

func (u *appServerUI) applyInput(surface *terminalsession.Attachment, effects iter.Seq[terminalsession.InputEffect]) error {
	if u == nil {
		return (*nativeSession)(nil).applyInput(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, surface, effects)
	}
	return u.nativeSession.applyInput(&u.attached, u.shell, &u.picker, &u.Editor, u.clock, &u.side, &u.statusPanel, &u.resumePicker, u.ctx, &u.dirty, &u.detailFiles, &u.quitRequested, surface, effects)
}

func (u *appServerUI) finishRestoredContent() error {
	return finishRestoredContent(&u.attached, &u.Client, &u.picker, &u.restoring, &u.review, u.message, &u.resumePending)
}

func (u *appServerUI) drainKeys(keys <-chan byte) error {
	return u.nativeSession.drainKeys(&u.attached, u.shell, &u.picker, &u.Editor, u.clock, &u.side, &u.statusPanel, &u.resumePicker, u.ctx, &u.dirty, &u.detailFiles, &u.quitRequested, keys)
}

func (u *appServerUI) input(event terminalsession.Input) (bool, error) {
	return u.nativeSession.input(&u.attached, u.shell, &u.picker, &u.Editor, u.clock, &u.side, &u.statusPanel, &u.resumePicker, u.ctx, &u.dirty, &u.detailFiles, event)
}

func (u *appServerUI) applyComposerInput(input terminalsession.ComposerInput) (bool, error) {
	return u.nativeSession.applyComposerInput(&u.attached, u.shell, &u.picker, &u.Editor, u.clock, &u.side, &u.statusPanel, &u.resumePicker, u.ctx, &u.dirty, &u.detailFiles, input)
}

// appServerUI assembles independently owned collaborators for native-session tests.
type appServerUI struct {
	nativeSession
	shell         *terminalsession.Attachment
	terminalViews terminalsession.Views
	preparePane   func(*scope.Store)
	renderStreams terminalsession.Streams
	detailFiles
	dirty bool
}

// applyMainKey settles typed Main effects before resuming terminal interpretation.
func (session *nativeSession) applyMainKey(attached *attachedPresentation, terminal *terminalsession.Attachment, picker *composerPicker, editor **composer.Editor, clock func() time.Time, side **sessioncontrol.Side, panel **appServerStatusReport, resumePicker **appServerResumePicker, parent context.Context, dirty *bool, files *detailFiles, next terminalsession.KeyContinuation) (bool, error) {
	defer func() {
		settleTestPresentation(attached, terminal, &session.Client, picker, editor, session.settings, session.serviceTiers, session.title, session.backendVersion, session.restoring != nil, (*side), clock, &session.questions, &session.approvals, session.overlayOpen(*panel, *resumePicker))
	}()
	if !prepareComposerInput(attached, terminal, &session.Client, picker, editor, session.settings, session.serviceTiers, session.title, session.backendVersion, session.restoring != nil, (*side), clock, &session.questions, &session.approvals) {
		return false, nil
	}
	// Acquisition stays application-owned; interpretation consumes its publication.
	if picker.Open && picker.Kind == "" && picker.completionTarget((*editor).Draft, attached.conversation.ShellMode()) != picker.Target {
		picker.refreshPicker(attached, clock, session.Client.Requests, (*editor), parent, session.skillEnvironment)
	}
	publishTestPresentation(attached, terminal, &session.Client, picker, editor, session.settings, session.serviceTiers, session.title, session.backendVersion, session.restoring != nil, (*side))
	refresh := false
	defer func() {
		if refresh {
			picker.refreshPicker(attached, clock, session.Client.Requests, (*editor), parent, session.skillEnvironment)
		}
	}()
	focused := terminal == nil || terminal.Focus == 0
	input := terminal.Main.ContinueKey(next, terminal.Prompts, transcriptView(attached.presentation, terminal), focused)
	for {
		refresh = refresh || input.Regular
		switch {
		case input.Prompt != nil:
			if handled, err := session.applyPromptInput(attached, terminal, picker, editor, clock, side, session.overlayOpen(*panel, *resumePicker), parent, *input.Prompt); handled || err != nil {
				return false, err
			}
		case input.Panel != nil:
			if input.ClearSelection && terminal != nil {
				terminal.Selection = nil
			}
			applyPanelAction(attached, clock, panel, session.sessionCapture, *input.Panel)
			(*dirty) = true
		case input.Menu != nil:
			handled := session.applyTestMenuResult(attached, terminal, picker, editor, side, clock, session.overlayOpen(*panel, *resumePicker), parent, *input.Menu)
			session.dispatchRewind(attached, picker, clock, parent)
			if handled {
				return false, nil
			}
		case input.Composer != nil:
			return session.applyComposerInput(attached, terminal, picker, editor, clock, side, panel, resumePicker, parent, dirty, files, *input.Composer)
		}
		if input.Next == nil {
			return false, nil
		}
		publishTestPresentation(attached, terminal, &session.Client, picker, editor, session.settings, session.serviceTiers, session.title, session.backendVersion, session.restoring != nil, (*side))
		input = terminal.Main.ContinueKey(*input.Next, terminal.Prompts, transcriptView(attached.presentation, terminal), focused)
	}
}

func (session *nativeSession) applyApprovalInput(attached *attachedPresentation, terminal *terminalsession.Attachment, picker *composerPicker, editor **composer.Editor, side *sessioncontrol.Side, clock func() time.Time, overlay bool, input terminalsession.PromptInput) (bool, error) {
	keyInput := input.PromptKey
	if err := attached.presentation.ApprovalInput(sessionpresentation.ApprovalInputIntent{Version: input.Version, ApprovalInput: keyInput.Approval}, session.approvals.Approvals); err != nil {
		setNotice(&attached.feedback, presentationTime(clock), err.Error(), true)
		return true, nil
	}
	a := session.approvals.handles[session.approvals.Pending[0]]
	switch keyInput.Action {
	case conversationui.PromptPass:
		return false, nil
	case conversationui.PromptHide:
		hideApprovals(&session.approvals, &attached.conversation.PromptEditor, (*editor))
		session.approvals.AutoOpen = false
	case conversationui.PromptSubmit:
		view := attached.presentation.View()
		return true, session.decideApproval(attached, picker, editor, side, clock, overlay, sessionpresentation.ApprovalIntent{Version: view.Version, Request: a.ID, Choice: a.Choices[a.Selected].ID, Reason: (*editor).Text})
	}
	return true, nil
}

func applyCloseSide(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, settings preferences.Applied, tiers *inferencegateway.Tiers, title string, backend string, restoring bool, side **sessioncontrol.Side, overlay bool, owned *map[string]bool, in sessionpresentation.CloseSideIntent) error {
	defer func() {
		settleTestPresentation(attached, terminal, control, picker, editor, settings, tiers, title, backend, restoring, (*side), clock, questions, approvals, overlay)
	}()
	if err := attached.presentation.AdmitCloseSide(in); err != nil {
		setNotice(&attached.feedback, presentationTime(clock), err.Error(), true)
		return nil
	}
	return closeSide(attached, control, editor, questions, approvals, side, owned)
}

// finishPaste inserts a bracketed paste as one edit. A lone path to an image
// file, as terminals paste for copied or dropped files, attaches that file
// instead, followed by a space as in Codex. The user's file is never removed.
func finishPaste(attached *attachedPresentation, terminal *terminalsession.Attachment, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, side **sessioncontrol.Side, owned *map[string]bool, text string) {
	if q := questions.Current(); q != nil && !attached.presentation.DisplayingSide() {
		q.Edit(!q.Note)
	}
	path, ok := pastedImagePath(text)
	if !ok {
		if text != "" {
			insertDraft(attached, (*editor), clock, (*owned), questions, approvals, (*side), text)
			(*editor).Run = composer.NoRun
		}
		return
	}
	insertImage(attached, terminal, editor, questions, approvals, clock, (*owned), (*side), path)
	(*editor).Run = composer.InsertRun // The separating space belongs to the attachment's edit.
	insertDraft(attached, (*editor), clock, (*owned), questions, approvals, (*side), " ")
	(*editor).Run = composer.NoRun
}

func applyPaste(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, side **sessioncontrol.Side, owned *map[string]bool, environment []string, parent context.Context, paste terminalsession.PasteResult) {
	if err := attached.presentation.AdmitPaste(paste.Intent, attached.conversation); err != nil {
		setNotice(&attached.feedback, presentationTime(clock), err.Error(), true)
		return
	}
	text := paste.Text
	menu := sessionpresentation.MenuCompletion
	if published := attached.presentation.View().Menu; published != nil {
		menu = published.Kind
	}
	switch {
	case approvals.Open && !attached.presentation.DisplayingSide():
		if a := approvals.calls()[0]; a.DenialChoice() >= 0 && text != "" {
			a.Selected = a.DenialChoice()
			insertDraft(attached, (*editor), clock, (*owned), questions, approvals, (*side), text)
			(*editor).Run = composer.NoRun
		}
	case menu == sessionpresentation.MenuManageSkills:
		picker.Query += text
	case menu == sessionpresentation.MenuSkills || menu == sessionpresentation.MenuCopy || menu == sessionpresentation.MenuSettings || menu == sessionpresentation.MenuArchive || menu == sessionpresentation.MenuRewind || menu == sessionpresentation.MenuSideReplace:
	default:
		finishPaste(attached, terminal, editor, questions, approvals, clock, side, owned, text)
	}
	picker.Dismissed, picker.Open = picker.completionTarget((*editor).Draft, attached.conversation.ShellMode()), false
	picker.refreshPicker(attached, clock, control.Requests, (*editor), parent, environment)
}

// Called only after withRawPane has joined its input reader and restored the
// terminal. The next pane invocation re-enters raw mode and repaints the UI.
// attachImage takes ownership of a clipboard file for cleanup.
func attachImage(attached *attachedPresentation, terminal *terminalsession.Attachment, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, owned *map[string]bool, side *sessioncontrol.Side, path string) {
	insertImage(attached, terminal, editor, questions, approvals, clock, (*owned), side, path)
	if (*owned) == nil {
		(*owned) = make(map[string]bool)
	}
	(*owned)[path] = true
}

// insertImage is its own undoable edit.
func insertImage(attached *attachedPresentation, terminal *terminalsession.Attachment, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, owned map[string]bool, side *sessioncontrol.Side, path string) {
	(*editor).InsertImage(path)
	terminal.Main.CursorColumn = nil
	setNotice(&attached.feedback, presentationTime(clock), "", false)
	pruneDraftImages(owned, (*editor), attached.conversation, questions, approvals, side, attached.hooks.nativeImages)
}

// Herdr's Codex detector consumes the same OSC 0 activity markers as Codex.
// Source: codex-rs/tui/src/chatwidget/status_surfaces.rs:28:43@1cc7e236
// TERMINAL_TITLE_SPINNER_FRAMES and TERMINAL_TITLE_ACTION_REQUIRED_TEXT.
func (n *nativeNotifications) writeTerminalTitle(feedback *noticepresentation.Model, terminal *terminalsession.Attachment, clock func() time.Time, workspace, main string, questions *prompt.Questions, approvals *prompt.Approvals, busy bool, now time.Time) {
	if n == nil || n.out == nil {
		return
	}
	n.writeTitle(feedback, clock, workspace, questions.Count(main) > 0 || len(n.blocked) > 0 || len(approvals.Pending) > 0, busy, now)
}

func moveQuestion(questions *nativeQuestionDock, conversation *conversationmodel.Session, terminal *terminalsession.Attachment, editor **composer.Editor, main string, step int) {
	parkQuestion(questions, *editor, main, questions.Current())
	questions.Move(step)
	*editor = resumeQuestion(questions, conversation, main, questions.Current())
}

func (session *nativeSession) applyQuestionInput(attached *attachedPresentation, terminal *terminalsession.Attachment, picker *composerPicker, editor **composer.Editor, side *sessioncontrol.Side, clock func() time.Time, overlay bool, parent context.Context, input terminalsession.PromptInput) (bool, error) {
	keyInput := input.PromptKey
	if err := attached.presentation.QuestionInput(sessionpresentation.QuestionInputIntent{Version: input.Version, QuestionInput: keyInput.Question}, session.questions.Questions); err != nil {
		setNotice(&attached.feedback, presentationTime(clock), err.Error(), true)
		return true, nil
	}
	q, c := session.questions.Current(), session.questions.activeCall()
	action := keyInput.Action
	switch action {
	case conversationui.PromptPass:
		return false, nil
	case conversationui.PromptHide:
		session.questions.AutoOpen = false
		(*editor) = hideQuestionEditor(&session.questions, attached.conversation, (*editor), session.Client.Thread)
	case conversationui.PromptCursor:
		return false, nil
	case conversationui.PromptPrevious:
		moveQuestion(&session.questions, attached.conversation, terminal, editor, session.Client.Thread, -1)
	case conversationui.PromptNext:
		moveQuestion(&session.questions, attached.conversation, terminal, editor, session.Client.Thread, 1)
	case conversationui.PromptSubmit:
		return true, session.submitQuestions(attached, picker, editor, side, clock, overlay, parent, sessionpresentation.QuestionSubmitIntent{Version: attached.presentation.View().Version, Call: c.ID, Incomplete: true})
	case conversationui.PromptAnswer, conversationui.PromptSkip:
		parkQuestion(&session.questions, (*editor), session.Client.Thread, q)
		if q.IsSecret {
			answer := *q
			if !answer.Complete((*editor).Text, c.Sync, action == conversationui.PromptSkip) {
				return true, nil
			}
			q.Done, q.Skipped = answer.Done, answer.Skipped
			session.questions.secretAnswers(c.ID).Answers[session.questions.Index] = answer.Answer
		} else {
			view := attached.presentation.View()
			if !session.answerQuestion(attached, picker, editor, side, clock, overlay, sessionpresentation.AnswerIntent{Version: view.Version,
				Call: c.ID, Question: view.Questions.Call.Questions[session.questions.Index].ID,
				Choice: q.Selected, Draft: q.Editor.Snapshot.Draft, Skip: action == conversationui.PromptSkip}) {
				return true, nil
			}
		}
		if step, submit := session.questions.Advance(); step != 0 {
			moveQuestion(&session.questions, attached.conversation, terminal, editor, session.Client.Thread, step)
		} else if submit {
			return true, session.submitQuestions(attached, picker, editor, side, clock, overlay, parent, sessionpresentation.QuestionSubmitIntent{Version: attached.presentation.View().Version, Call: c.ID})
		}
	}
	return true, nil
}

// transcriptView selects display state without changing the input or execution owner.
func transcriptView(presentation *sessionpresentation.Model, terminal *terminalsession.Attachment) *activityui.View {
	view := presentation.View()
	if terminal.Agents != nil && view.Displayed.ID != view.Main.ID {
		terminal.Agents.Conversation = true
		return terminal.Agents
	}
	return terminal.Transcript
}

// openOutput opens the operation a snippet names in the dialog, reporting
// whether it names one.
func openOutput(attached *attachedPresentation, terminal *terminalsession.Attachment, files *detailFiles, changes *changecapture.Service, u *terminalsession.Attachment, view *activityui.View, snippet activityui.Snippet) bool {
	block, ok := view.SnippetBlock(snippet)
	if !ok || block.EditSource != "" || len(block.Questions) > 0 {
		return false
	}
	pages := activitypresentation.DetailPages([]activitypresentation.Value{block.Value})
	if len(block.Members) > 0 {
		pages = nil
		seen := make(map[uint64]bool)
		for _, value := range block.Members {
			member := activityui.Block{Value: value}
			if member.EditSource != "" {
				if seen[member.Source] {
					continue
				}
				if edits, _ := activityEditPages(attached, activitySource(attached, terminal, view), changes, member.Source, ""); len(edits) > 0 {
					pages = append(pages, edits...)
					seen[member.Source] = true
					continue
				}
			}
			pages = append(pages, activitypresentation.DetailPages([]activitypresentation.Value{member.Value})...)
		}
	}
	openDetails(attached, terminal, files, u, view, pages)
	page := 0
	if block.Output != 0 && (len(block.Members) == 0 || block.Kind == "op") {
		for i, command := range u.Dialog.Pages {
			if command.Source == block.Source && command.Output == block.Output {
				page = i
				break
			}
		}
	}
	u.Dialog.ShowPage(page)
	return true
}

func (session *nativeSession) outputIntent(attached *attachedPresentation, terminal *terminalsession.Attachment, picker *composerPicker, editor **composer.Editor, clock func() time.Time, side **sessioncontrol.Side, panel **appServerStatusReport, resumePicker **appServerResumePicker, parent context.Context, files *detailFiles, surface *terminalsession.Attachment, intent terminalsession.DialogIntent) {
	switch intent.Action {
	case terminalsession.DialogClose:
		files.closeTerminalOutput(surface)
	case terminalsession.DialogCopy:
		copyText(&attached.feedback, clock, surface, intent.Text)
	case terminalsession.DialogReference:
		if !session.referenceInput(attached, picker, editor, clock, side, panel, resumePicker, parent, terminal.Main.Reference("dialog", intent.Text, "")) {
			return
		}
		surface.Focus = 0
		files.closeTerminalOutput(surface)
	case terminalsession.DialogOpenLink:
		files.openTerminalMarkdownFile(&attached.session, attached.presentation, attached.cwd, activitySource(attached, terminal, surface.Dialog.View), surface, surface.Dialog.View, intent.Text)
	}
}

// applyComposerInput resolves terminal output through the current owners. Every
// semantic action retains the publication and editor target it came from.
func (session *nativeSession) applyComposerInput(attached *attachedPresentation, terminal *terminalsession.Attachment, picker *composerPicker, editor **composer.Editor, clock func() time.Time, side **sessioncontrol.Side, panel **appServerStatusReport, resumePicker **appServerResumePicker, parent context.Context, dirty *bool, files *detailFiles, input terminalsession.ComposerInput) (bool, error) {
	if input.Err != nil {
		setNotice(&attached.feedback, presentationTime(clock), input.Err.Error(), true)
		return false, nil
	}
	if input.Changed {
		if q := session.questions.Current(); q != nil && q.IsSecret {
			session.questions.secret = terminal.Prompts.Secret
		}
	}
	if input.Command.Operation != sessionpresentation.NoComposerOperation {
		defer func() {
			files.prepareOutput(&attached.session, terminal)
			if input.Command.Operation == sessionpresentation.SubmitComposer {
				terminal.Transcript.Following = true
			}
		}()
		return session.applyComposerCommand(attached, picker, editor, clock, side, panel, resumePicker, parent, files, input.Command, nil)
	}
	kind, changed := input.LocalEdit, input.Changed
	if input.Editor.Action.Kind != composer.NoAction {
		var err error
		changed, err = attached.presentation.ApplyEditor(input.Editor, attached.conversation)
		if err != nil {
			setNotice(&attached.feedback, presentationTime(clock), err.Error(), true)
			return false, nil
		}
		kind = input.Editor.Action.Kind
	}
	switch kind {
	case composer.NoAction, composer.MoveAction:
		return false, nil
	case composer.RecallPreviousAction, composer.RecallNextAction:
		if !changed {
			return false, nil
		}
		terminal.Main.CursorColumn = nil
	case composer.DeleteAction:
		if !changed {
			return false, nil
		}
	}
	setNotice(&attached.feedback, presentationTime(clock), "", false)
	if kind != composer.UndoAction && kind != composer.RedoAction {
		pruneDraftImages(session.ownedImages, (*editor), attached.conversation, &session.questions, &session.approvals, (*side), attached.hooks.nativeImages)
	}
	return false, nil
}

func (session *nativeSession) applyPromptInput(attached *attachedPresentation, terminal *terminalsession.Attachment, picker *composerPicker, editor **composer.Editor, clock func() time.Time, side **sessioncontrol.Side, overlay bool, parent context.Context, input terminalsession.PromptInput) (bool, error) {
	if input.Approval.Request != "" {
		return session.applyApprovalInput(attached, terminal, picker, editor, (*side), clock, overlay, input)
	}
	return session.applyQuestionInput(attached, terminal, picker, editor, (*side), clock, overlay, parent, input)
}

// displaySession accepts detached targets without consulting terminal state.
func displaySession(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, settings preferences.Applied, tiers *inferencegateway.Tiers, title string, backend string, restoring bool, side *sessioncontrol.Side, clock func() time.Time, questions *nativeQuestionDock, approvals *nativeApprovalDock, overlay bool, intent sessionpresentation.DisplayIntent) bool {
	defer func() {
		settleTestPresentation(attached, terminal, control, picker, editor, settings, tiers, title, backend, restoring, side, clock, questions, approvals, overlay)
	}()
	m := attached.presentation
	if intent.Thread != m.View().Main && intent.Thread != m.View().SideThread && attached.session.KnownCaller(intent.Thread.ID) != intent.Thread.Caller {
		setNotice(&attached.feedback, presentationTime(clock), sessionpresentation.ErrStaleIntent.Error(), true)
		return false
	}
	crossesSide := m.View().Displayed != intent.Thread && (m.DisplayingSide() || intent.Thread == m.View().SideThread)
	if err := m.Display(intent); err != nil {
		setNotice(&attached.feedback, presentationTime(clock), err.Error(), true)
		return false
	}
	if crossesSide {
		picker.sideTask = nil
		picker.Open, picker.Kind, picker.Choices = false, "", nil
		picker.cancelScan()
	}
	return true
}

func (u *appServerUI) restoreContent(root binding.ThreadInfo) error {
	return u.nativeSession.restorePaneContent(&u.attached, &u.picker, &u.Editor, u.clock, &u.statusPanel, &u.resumePicker, u.ctx, root)
}

func registerThread(session *sessionpresentation.Attachment, info binding.ThreadInfo) {
	session.RegisterThread(info.Observation())
	session.Activity().Agent(info.ID).ParentThread = info.ParentThreadID
	restoreThreadSettings(session, info)
}
