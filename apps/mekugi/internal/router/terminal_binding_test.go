package router

import (
	"context"
	"io"
	"iter"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
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
	u.main.outputIntent(u.Attachment, intent)
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
	u.main.openSelectionTarget(u.Attachment, target)
}
func (u *terminalUI) openActivityReply(seq uint64) bool {
	return u.main.openActivityReply(u.Attachment, seq)
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
	return paintNative(u.Attachment, u.frameInput(), view, activitySource(&u.main.attached, u.main.shell, view), ctx, out)
}
func (u *terminalUI) openErrors() {
	if u.Focus == 0 && u.main != nil {
		u.main.openErrorDetails(u.Attachment, transcriptView(u.main.attached.presentation, u.main.shell))
	}
}
