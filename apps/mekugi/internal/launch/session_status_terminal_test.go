package launch

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"

	terminalsession "github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
)

func (u *appServerUI) sessionLabel(now time.Time) string {
	return terminalsession.PaintSessionStatus(publishPresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, now).Status.Session, now, u.shell.Transcript.Painter.Colors)
}
func (u *appServerUI) stateLabel(now time.Time) string {
	return terminalsession.PaintMainStatus(publishPresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, now).Status, now, u.shell.Transcript.Painter.Colors)
}
func (u *appServerUI) composerErrorText() string {
	return terminalsession.MainStatusDetails(publishPresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, presentationTime(u.clock)).Status)
}

// testMainFrame settles directly configured fixture state before rendering.
func (u *appServerUI) testMainFrame(width, height, dock int) ([]string, terminalsession.Rect) {
	settlePresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, u.clock, &u.questions, &u.approvals, u.statusPanel != nil || u.resumePicker != nil)
	return u.mainFrame(width, height, dock)
}

// testPaint delivers an explicitly settled fixture through the real renderer.
func (u *appServerUI) testPaint(out io.Writer, width, height int) error {
	settlePresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, u.clock, &u.questions, &u.approvals, u.statusPanel != nil || u.resumePicker != nil)
	return paintSession(&u.attached, u.shell, &u.detailFiles, u.ctx, presentationTime(u.clock), out, width, height)
}

func (u *appServerUI) deleteDraft(backward bool) {
	key := "\x1b[3~"
	if backward {
		key = "\x7f"
	}
	u.applyComposerKey(key)
}
func (u *appServerUI) undoDraft(redo bool) {
	key := "\x1a"
	if redo {
		key = "\x19"
	}
	u.applyComposerKey(key)
}

func (u *appServerUI) recallInput(backward bool) {
	if u.Editor.Recall(backward) {
		u.shell.Main.CursorColumn = nil
		setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), "", false)
		pruneDraftImages(u.ownedImages, u.Editor, u.attached.conversation, &u.questions, &u.approvals, u.shell.Prompts, u.side, u.attached.nativeImages)
	}
}

func (u *appServerUI) mainHeaderRight(width int, focused bool) string {
	return u.shell.Main.HeaderRight(u.shell.Transcript, width, focused)
}

// mainFrame hydrates identified output between terminal-only render passes.
func (u *appServerUI) mainFrame(width, height, dock int) ([]string, terminalsession.Rect) {
	frame := u.renderMainFrame(width, height, dock)
	return frame.Rows, frame.Dock
}

func (u *appServerUI) renderMainFrame(width, height, dock int) terminalsession.MainFrame {
	transcript := transcriptView(u.attached.presentation, u.shell)
	options := terminalsession.MainOptions{Now: presentationTime(u.clock), Focused: terminalTest(u) == nil || terminalTest(u).Focus == 0, Version: mekugiVersion()}
	for {
		frame := u.shell.Main.Render(transcript, &u.shell.Transcript.Painter, u.shell.Prompts, options, width, height, dock)
		if len(frame.Demands) == 0 {
			return frame
		}
		transcript.Hydrate(activitySource(&u.attached, u.shell, transcript).ResolveOutputDemands(frame.Demands))
	}
}

func (u *terminalUI) paintSelection(rows []string) {
	if !u.Frame.PaintSelection(u.frameInput(), rows) {
		u.Selection = nil
	}
}

func (u *appServerUI) testDeliveredMainFrame(t *testing.T, width, height, dock int) ([]string, terminalsession.NoticeReceipt) {
	t.Helper()
	settlePresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, u.clock, &u.questions, &u.approvals, u.statusPanel != nil || u.resumePicker != nil)
	frame := u.renderMainFrame(width, height, dock)
	var delivered terminalsession.Frame
	if err := delivered.Write(io.Discard, terminalsession.RenderedFrame{Rows: frame.Rows, Notices: frame.Notices}, width, true); err != nil {
		t.Fatal(err)
	}
	return frame.Rows, delivered.Notices
}

func (u *appServerUI) applyComposerKey(key string) (bool, error) {
	publishPresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, presentationTime(u.clock))
	return u.applyComposerInput(u.shell.Main.ComposerKey(key, u.shell.Prompts, transcriptView(u.attached.presentation, u.shell)))
}
func (u *appServerUI) approvalKey(key string) (bool, error) {
	publishPresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, presentationTime(u.clock))
	input := u.shell.Main.PromptKey(key, u.shell.Prompts)
	if input == nil {
		return false, nil
	}
	return applyApprovalInput(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, u.clock, &u.questions, &u.approvals, u.statusPanel != nil || u.resumePicker != nil, u.notifications, u.review, &u.pendingEditReceipts, u.controlObservations, *input)
}
func (u *appServerUI) questionKey(key string) (bool, error) {
	publishPresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, presentationTime(u.clock))
	input := u.shell.Main.PromptKey(key, u.shell.Prompts)
	if input == nil {
		return false, nil
	}
	return applyQuestionInput(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, u.clock, &u.questions, &u.approvals, u.statusPanel != nil || u.resumePicker != nil, u.skillEnvironment, &u.ownedImages, &u.guardHookCheck, u.ctx, u.controlObservations, u.review, &u.pendingEditReceipts, *input)
}

func paintSession(attached *attachedPresentation, terminal *terminalsession.Attachment, files *detailFiles, ctx context.Context, at time.Time, out io.Writer, width, height int) error {
	terminal.Width, terminal.Height = max(1, width), max(1, height)
	terminal.Main.ContentPainted = false
	files.prepareOutput(&attached.session, terminal)
	if ctx == nil {
		ctx = context.Background()
	}
	view := transcriptView(attached.presentation, terminal)
	return paintNative(files, terminal, terminal.FrameInput(at, mekugiVersion()), view, activitySource(attached, terminal, view), ctx, out)
}
