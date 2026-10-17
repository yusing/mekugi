package router

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
)

func setSideTest(u *appServerUI, side *sessioncontrol.Side) {
	u.attached.conversation.Side = side.SideAnswer
	u.side = side
	u.settlePresentation()
}

func (u *appServerUI) btwRows(width, height int) []string {
	u.settlePresentation()
	if !u.shell.Main.Side.Open() || height < 1 {
		return nil
	}
	return u.shell.Main.Side.Render(&u.shell.Transcript.Painter, presentationTime(u.clock), width, height).Rows
}
