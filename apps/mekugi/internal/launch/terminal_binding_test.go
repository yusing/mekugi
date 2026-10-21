package launch

import (
	"context"
	"io"
	"iter"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
)

type terminalUI struct {
	*terminalsession.Attachment
	main *appServerUI
}

func terminalTest(main *appServerUI) *terminalUI {
	if main.shell == nil {
		return nil
	}
	return &terminalUI{Attachment: main.shell, main: main}
}

func (u *terminalUI) openOutput(view *activityui.View, snippet activityui.Snippet) bool {
	return openOutput(&u.main.attached, u.main.shell, &u.main.detailFiles, u.main.activity, u.Attachment, view, snippet)
}
func (u *terminalUI) openBlocks(view *activityui.View, pages []activityui.Block) {
	openBlocks(&u.main.attached, u.main.shell, &u.main.detailFiles, u.Attachment, view, pages)
}
func (u *terminalUI) openEntry(view *activityui.View, seq uint64) bool {
	return openEntry(&u.main.attached, u.main.shell, &u.main.detailFiles, u.Attachment, view, seq)
}
func (u *terminalUI) outputIntent(intent terminalsession.DialogIntent) {
	outputIntent(&u.main.attached, u.main.shell, &u.main.Client, &u.main.picker, &u.main.Editor, &u.main.questions, &u.main.approvals, u.main.clock, &u.main.childHistoryState, preferences.Applied{Model: u.main.model, Effort: u.main.reasoningEffort, Tier: u.main.serviceTier}, u.main.serviceTiers, u.main.title, u.main.backendVersion, &u.main.restoring, &u.main.side, &u.main.statusPanel, &u.main.resumePicker, &u.main.ownedImages, u.main.skillEnvironment, u.main.ctx, &u.main.detailFiles, u.Attachment, intent)
}
func (u *terminalUI) openActivityEdit(view *activityui.View, seq uint64, path string) bool {
	return openActivityEdit(&u.main.attached, u.main.shell, &u.main.detailFiles, u.main.activity, u.Attachment, view, seq, path)
}
func (u *terminalUI) copyText(text string) {
	copyText(&u.main.attached.feedback, u.main.shell, u.main.clock, u.Attachment, text)
}
func (u *terminalUI) applyInput(effects iter.Seq[terminalsession.InputEffect]) error {
	return u.main.applyInput(u.Attachment, effects)
}
func (u *terminalUI) openSelectionTarget(target terminalsession.SelectionTarget) {
	openSelectionTarget(&u.main.attached, u.main.shell, u.main.clock, u.main.activity, &u.main.detailFiles, u.Attachment, target)
}
func (u *terminalUI) openActivityReply(seq uint64) bool {
	return openActivityReply(&u.main.attached, u.main.shell, &u.main.detailFiles, u.Attachment, seq)
}
func (u *terminalUI) frameInput() terminalsession.FrameInput {
	var now time.Time
	if u.main != nil {
		now = presentationTime(u.main.clock)
	}
	return u.Attachment.FrameInput(now, mekugiVersion())
}
func (u *terminalUI) paintNative(ctx context.Context, out io.Writer) error {
	view := transcriptView(u.main.attached.presentation, u.main.shell)
	return paintNative(&u.main.detailFiles, u.Attachment, u.frameInput(), view, activitySource(&u.main.attached, u.main.shell, view), ctx, out)
}
func (u *terminalUI) openErrors() {
	if u.Focus == 0 && u.main != nil {
		openErrorDetails(&u.main.attached, u.main.shell, &u.main.detailFiles, u.Attachment, transcriptView(u.main.attached.presentation, u.main.shell))
	}
}
