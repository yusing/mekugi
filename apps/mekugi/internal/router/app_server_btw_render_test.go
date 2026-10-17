package router

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
)

func setSideTest(u *appServerUI, side *sessioncontrol.Side) {
	u.conversation().Side = side.SideAnswer
	u.side, u.btw = side, new(sideDock)
	u.settlePresentation()
}

func (u *appServerUI) btwRows(width, height int) []string {
	b := u.btw
	if b == nil || height < 1 {
		return nil
	}
	u.settlePresentation()
	view := u.sessionPresentation().View()
	if view.Side == nil {
		return nil
	}
	frame := terminalsession.RenderSide(terminalsession.SideInput{View: *view.Side, Running: view.SideRunning, Scroll: b.scroll}, &u.view.Painter, u.now(), width, height)
	b.copyAnswer, b.rows, b.scroll = frame.CopyAnswer, frame.AnswerRows, frame.Scroll
	return frame.Rows
}
