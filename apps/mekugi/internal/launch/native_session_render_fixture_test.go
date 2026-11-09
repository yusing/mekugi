package launch

import (
	"context"

	"github.com/yusing/mekugi/libraries/textsanitize"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	noticepresentation "github.com/yusing/mekugi/modules/notifications/presentation"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/scope"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
	changepresentation "github.com/yusing/mekugi/modules/changehistory/presentation"
	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
	previewview "github.com/yusing/mekugi/modules/livepreview/terminalview"

	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	"github.com/yusing/mekugi/modules/conversation/composer"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
)

// Renderer fixtures consume semantic publications explicitly. Production session
// transitions never need a terminal to publish, switch editors or open prompts.
func renderTestPresentation(attached *attachedPresentation, terminal *terminalsession.Attachment, view sessionpresentation.View, editor *composer.Editor) {
	terminal.Main.InterruptLocked, terminal.Main.PredictionsDisabled = attached.interruptLocked, attached.predictionsDisabled
	terminal.LiveHidden = attached.liveHidden
	terminal.Prompts.Update(view)
	terminal.Main.Update(view)
	if q := attached.conversation.Questions.Current(); q != nil && q.IsSecret {
		terminal.Prompts.Secret = *editor
	}
	for _, v := range []*activityui.View{terminal.Transcript, terminal.Agents} {
		if v != nil {
			v.Painter.FileLink = markdownFileLinks(view.Info.Workspace)
			v.Painter.DeferFileLinks = true
		}
	}
	publishActivity(attached, terminal)
}

func publishTestPresentation(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, settings preferences.Applied, tiers *inferencegateway.Tiers, title string, backend string, restoring bool, side *sessioncontrol.Side) sessionpresentation.View {
	view := publishPresentation(attached, control, picker, editor, settings, tiers, title, backend, restoring, side)
	renderTestPresentation(attached, terminal, view, *editor)
	return view
}

func settleTestPresentation(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, settings preferences.Applied, tiers *inferencegateway.Tiers, title string, backend string, restoring bool, side *sessioncontrol.Side, clock func() time.Time, questions *nativeQuestionDock, approvals *nativeApprovalDock, overlay bool) {
	settlePresentation(attached, control, picker, editor, settings, tiers, title, backend, restoring, side, clock, questions, approvals, overlay)
	renderTestPresentation(attached, terminal, attached.presentation.View(), *editor)
}

func (p *nativePreferences) openTerminal(shell *terminalsession.Shell, main *terminalsession.Main, workspace, thread string, resume bool) error {
	initial, err := p.encoding.Encode(shell.Preferences(main))
	if err != nil {
		return err
	}
	if err := p.open(workspace, thread, resume); err != nil {
		return err
	}
	state := p.attachment.Snapshot()
	if !resume || state.Presentation == nil {
		state.Presentation = initial
	} else {
		layout, err := terminalsession.ReadPreferences(state.Presentation)
		if err != nil {
			return err
		}
		shell.RestorePreferences(layout, main)
		state.Presentation, err = p.encoding.Encode(shell.Preferences(main))
		if err != nil {
			return err
		}
	}
	p.attachment.Restore(state)
	return nil
}

func applyTestPreview(attached *attachedPresentation, terminal *terminalsession.Attachment, review *sessionreview.Review, pending *[]*changepresentation.Receipt, at time.Time, preview previewmodel.Preview, restored bool) {
	applyPreview(attached, review, pending, preview)
	if terminal != nil {
		terminal.Live.Observe(preview, restored, at)
	}
}

func applyTestReview(attached *attachedPresentation, terminal *terminalsession.Attachment, review *sessionreview.Review, source *changecapture.Service, prepare func(*scope.Store), pending *[]*changepresentation.Receipt, clock func() time.Time, ctx context.Context, event sessionreview.Event) {
	applyReview(attached, review, source, pending, ctx, event)
	if terminal == nil {
		return
	}
	switch event.Kind {
	case "preview":
		terminal.Live.Observe(*event.Preview, event.Resync, presentationTime(clock))
	case "scope":
		if event.Resync && event.Scope != nil {
			terminal.Live.Resync(*event.Scope, event.PreviewIDs)
		}
	case "change":
		terminal.DiffUnseen = terminal.DiffUnseen || !terminal.DiffOpen
	}
	if prepare != nil {
		prepare(attached.session.Scope())
	}
}

func admitTestReviewEvents(attached *attachedPresentation, terminal *terminalsession.Attachment, review *sessionreview.Review, source *changecapture.Service, prepare func(*scope.Store), pending *[]*changepresentation.Receipt, clock func() time.Time, ctx context.Context, sub *sessionreview.Subscription, events ...sessionreview.Event) bool {
	if sub.Invalidated() {
		return false
	}
	for _, event := range events {
		applyTestReview(attached, terminal, review, source, prepare, pending, clock, ctx, event)
	}
	return review.Ready()
}

func loadedTestReview(attached *attachedPresentation, terminal *terminalsession.Attachment, review *sessionreview.Review, source *changecapture.Service, prepare func(*scope.Store), pending *[]*changepresentation.Receipt, result sessionreview.Result) {
	loadedReview(attached, review, source, pending, result)
	if terminal != nil && prepare != nil {
		prepare(attached.session.Scope())
	}
}

func publishTestReview(attached *attachedPresentation, terminal *terminalsession.Attachment, review *sessionreview.Review, source *changecapture.Service, prepare func(*scope.Store)) {
	publishReview(attached, review, source)
	if terminal != nil && prepare != nil {
		prepare(attached.session.Scope())
	}
}

func resetTestDiffScope(attached *attachedPresentation, terminal *terminalsession.Attachment, review *sessionreview.Review, source *changecapture.Service, prepare func(*scope.Store), parent context.Context) {
	resetDiffScope(attached, review, source, parent)
	if terminal != nil {
		if terminal.Auxiliary != nil {
			terminal.Auxiliary.Reset()
		}
		terminal.Live, terminal.DiffUnseen = previewview.PreviewPane{}, false
		if prepare != nil {
			prepare(attached.session.Scope())
		}
	}
}

func restoreTestDiffThread(attached *attachedPresentation, terminal *terminalsession.Attachment, review *sessionreview.Review, source *changecapture.Service, prepare func(*scope.Store), info binding.ThreadInfo) {
	restoreDiffThread(attached, review, source, info)
	if terminal != nil && prepare != nil {
		prepare(attached.session.Scope())
	}
}

func (session *appServerUI) flushTestStreamOutput(attached *attachedPresentation, terminal *terminalsession.Attachment, clock func() time.Time, parent context.Context) {
	session.flushStreamOutput(attached, clock, parent)
	session.renderStreams.AdvanceReasoning(presentationTime(clock), terminal.Transcript, terminal.Agents)
	session.renderStreams.AdvanceCommands(presentationTime(clock), nil, terminal.Transcript, terminal.Agents)
	publishActivity(attached, terminal)
}

func (session *nativeSession) applyTestMenuResult(attached *attachedPresentation, terminal *terminalsession.Attachment, picker *composerPicker, editor **composer.Editor, side **sessioncontrol.Side, clock func() time.Time, overlay bool, parent context.Context, result terminalsession.MenuResult) bool {
	var effects nativeViewEffects
	if attached.effects == nil && picker.Kind == "copy" {
		attached.effects = &effects
		defer func() {
			attached.effects = nil
			for _, effect := range effects.publication() {
				if effect.Clipboard != nil {
					copyText(&attached.feedback, clock, terminal, *effect.Clipboard)
				}
			}
		}()
	}
	handled := session.applyMenuResult(attached, picker, editor, side, clock, overlay, parent, result)
	renderTestPresentation(attached, terminal, attached.presentation.View(), *editor)
	return handled
}

func (files *detailFiles) replaceTerminalDetails(session *sessionpresentation.Attachment, terminal *terminalsession.Attachment, pages []activitypresentation.DetailPage) {
	if terminal.Dialog == nil {
		return
	}
	files.replaceDetails(session, pages)
	terminal.Dialog.Invalidate()
	files.prepareOutput(session, terminal)
}

func publishActivity(attached *attachedPresentation, terminal *terminalsession.Attachment) {
	attached.session.ProjectSide(attached.childSource, attached.presentation.View().SideThread, attached.conversation.Side)
	refreshRosterRoles(&attached.session, attached.childSource)
	session := attached.presentation
	if terminal.Transcript != nil {
		terminal.Transcript.StatusLabel = textsanitize.Safe(terminalsession.StatusText(session.View().Status.State), false)
	}
	deliverActivity(terminal.Transcript, attached.source, session, nil, nil)
	deliverActivity(terminal.Agents, attached.childSource, session, attached.source, terminal.Transcript)
}

// activitySource resolves only a renderer already bound to this attachment.
func activitySource(attached *attachedPresentation, terminal *terminalsession.Attachment, view *activityui.View) *sessionpresentation.ActivitySource {
	if view == terminal.Transcript {
		return attached.source
	}
	if view == terminal.Agents {
		return attached.childSource
	}
	return nil
}

func (p *nativePreferences) stage(layout terminalsession.Preferences) error {
	state, err := p.encoding.Encode(layout)
	if err != nil {
		return err
	}
	p.attachment.StagePresentation(state)
	return nil
}

// openErrorDetails exposes the selected transcript's errors through the shared content
// dialog, newest first on opening. Left/Right reaches earlier retained errors.
func openErrorDetails(attached *attachedPresentation, terminal *terminalsession.Attachment, files *detailFiles, surface *terminalsession.Attachment, view *activityui.View) {
	if view == nil {
		return
	}
	files.openErrorDetails(attached, activitySource(attached, terminal, view))
	surface.Selection, surface.Dialog = nil, nil
	files.prepareOutput(&attached.session, surface)
	if surface.Dialog != nil {
		surface.Dialog.ShowPage(len(surface.Dialog.Pages) - 1)
	}
}

func openSkills(attached *attachedPresentation, terminal *terminalsession.Attachment, files *detailFiles, surface *terminalsession.Attachment, view *activityui.View, agent string) {
	files.openSkills(attached, activitySource(attached, terminal, view), agent)
	surface.Selection, surface.Dialog = nil, nil
	files.prepareOutput(&attached.session, surface)
}

// openActivityEdit resolves an exact admitted invocation without consulting the
// Diff painter, retained storage, or current workspace contents.
func openActivityEdit(attached *attachedPresentation, terminal *terminalsession.Attachment, files *detailFiles, changes *changecapture.Service, u *terminalsession.Attachment, view *activityui.View, seq uint64, path string) bool {
	if !files.openActivityEdit(attached, activitySource(attached, terminal, view), changes, seq, path) {
		return false
	}
	u.Selection = nil
	u.Dialog = nil
	files.prepareOutput(&attached.session, u)
	if u.Dialog != nil {
		for i, page := range u.Dialog.Pages {
			if page.ID == files.selected {
				u.Dialog.ShowPage(i)
				break
			}
		}
	}
	return true
}

// A Main excerpt carries an Activity identity, not a nearby question or run.
func openActivityReply(attached *attachedPresentation, terminal *terminalsession.Attachment, files *detailFiles, surface *terminalsession.Attachment, seq uint64) bool {
	if !files.openActivityReply(attached, seq) {
		return false
	}
	surface.Selection, surface.Dialog = nil, nil
	files.prepareOutput(&attached.session, surface)
	return true
}

// openBlocks is the shared modal entry point. It never changes pane navigation.
func openBlocks(attached *attachedPresentation, terminal *terminalsession.Attachment, files *detailFiles, u *terminalsession.Attachment, view *activityui.View, pages []activityui.Block) {
	openDetails(attached, terminal, files, u, view, activitypresentation.DetailPages(activityui.Values(pages)))
}

func openDetails(attached *attachedPresentation, terminal *terminalsession.Attachment, files *detailFiles, u *terminalsession.Attachment, view *activityui.View, pages []activitypresentation.DetailPage) {
	if len(pages) == 0 {
		return
	}
	files.openTerminalDetailSelection(&attached.session, attached.presentation.View().Version, activitySource(attached, terminal, view), u, view, pages, false)
}

func openEntry(attached *attachedPresentation, terminal *terminalsession.Attachment, files *detailFiles, u *terminalsession.Attachment, view *activityui.View, seq uint64) bool {
	pages := entryDetailPages(activitySource(attached, terminal, view), seq)
	openDetails(attached, terminal, files, u, view, pages)
	return len(pages) > 0
}

// Clipboard delivery belongs to the terminal; feedback uses the shared notice owner.
func copyText(feedback *noticepresentation.Model, clock func() time.Time, u *terminalsession.Attachment, text string) {
	notice, warning := u.Frame.CopyText(text)
	setNotice(feedback, presentationTime(clock), notice, warning)
}
