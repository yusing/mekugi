package router

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
)

func setSideTest(u *appServerUI, side *sessioncontrol.Side) {
	u.conversation().Side = side.SideAnswer
	u.side = side
	u.settlePresentation()
}

func (u *appServerUI) btwRows(width, height int) []string {
	u.settlePresentation()
	if !u.mainView.Side.Open() || height < 1 {
		return nil
	}
	return u.mainView.Side.Render(&u.view.Painter, u.now(), width, height).Rows
}
