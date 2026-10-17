package router

import (
	"io"
	"time"

	terminalsession "github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
)

func (u *appServerUI) sessionLabel(now time.Time) string {
	return terminalsession.PaintSessionStatus(u.publishPresentation(now).Status.Session, now, u.view.Painter.Colors)
}
func (u *appServerUI) stateLabel(now time.Time) string {
	return terminalsession.PaintMainStatus(u.publishPresentation(now).Status, now, u.view.Painter.Colors)
}
func (u *appServerUI) composerErrorText() string {
	return terminalsession.MainStatusDetails(u.publishPresentation(u.now()).Status)
}

// testMainFrame settles directly configured fixture state before rendering.
func (u *appServerUI) testMainFrame(width, height, dock int) ([]string, terminalsession.Rect) {
	u.settlePresentation()
	return u.mainFrame(width, height, dock)
}

// testPaint delivers an explicitly settled fixture through the real renderer.
func (u *appServerUI) testPaint(out io.Writer, width, height int) error {
	u.settlePresentation()
	return u.paint(out, width, height)
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
		u.mainView.CursorColumn = nil
		u.setNotice("", false)
		u.pruneDraftImages()
	}
}
