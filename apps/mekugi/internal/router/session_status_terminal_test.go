package router

import (
	"time"

	terminalsession "github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
)

func (u *appServerUI) sessionLabel(now time.Time) string {
	return terminalsession.PaintSessionStatus(u.mainPresentation(now).Status.Session, now, u.view.Painter.Colors)
}
func (u *appServerUI) stateLabel(now time.Time) string {
	return terminalsession.PaintMainStatus(u.mainPresentation(now).Status, now, u.view.Painter.Colors)
}
func (u *appServerUI) composerErrorText() string {
	return terminalsession.MainStatusDetails(u.mainPresentation(u.now()).Status)
}
