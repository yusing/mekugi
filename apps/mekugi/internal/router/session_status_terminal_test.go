package router

import (
	"io"
	"testing"
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

func (u *appServerUI) mainHeaderRight(width int, focused bool) string {
	return u.mainView.HeaderRight(u.view, width, focused)
}

// mainFrame hydrates identified output between terminal-only render passes.
func (u *appServerUI) mainFrame(width, height, dock int) ([]string, terminalsession.Rect) {
	frame := u.renderMainFrame(width, height, dock)
	return frame.Rows, frame.Dock
}

func (u *appServerUI) renderMainFrame(width, height, dock int) terminalsession.MainFrame {
	transcript := u.transcriptView()
	options := terminalsession.MainOptions{Now: u.now(), Focused: u.shell == nil || u.shell.Focus == 0, Version: mekugiVersion()}
	for {
		frame := u.mainView.Render(transcript, &u.view.Painter, &u.prompts, options, width, height, dock)
		if len(frame.Demands) == 0 {
			return frame
		}
		transcript.Hydrate(u.activitySource(transcript).ResolveOutputDemands(frame.Demands))
	}
}

func (u *terminalUI) paintSelection(rows []string) {
	if !u.frame.PaintSelection(u.frameInput(), rows) {
		u.selection = nil
	}
}

func (u *appServerUI) testDeliveredMainFrame(t *testing.T, width, height, dock int) ([]string, terminalsession.NoticeReceipt) {
	t.Helper()
	u.settlePresentation()
	frame := u.renderMainFrame(width, height, dock)
	var delivered terminalsession.Frame
	if err := delivered.Write(io.Discard, terminalsession.RenderedFrame{Rows: frame.Rows, Notices: frame.Notices}, width, true); err != nil {
		t.Fatal(err)
	}
	return frame.Rows, delivered.Notices
}
