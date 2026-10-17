package router

import "github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"

func (u *appServerUI) btwRows(width, height int) []string {
	b := u.btw
	if b == nil || height < 1 {
		return nil
	}
	frame := terminalsession.RenderSide(terminalsession.SideInput{View: b.View(), Running: b.Busy || b.Starting, Scroll: b.scroll}, &u.view.Painter, u.now(), width, height)
	b.copyAnswer, b.rows, b.scroll = frame.CopyAnswer, frame.AnswerRows, frame.Scroll
	return frame.Rows
}
