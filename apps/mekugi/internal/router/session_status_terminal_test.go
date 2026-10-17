package router

import "time"

func (u *appServerUI) sessionLabel(now time.Time) string {
	return paintSessionStatus(u.mainPresentation(now).Status.Session, now, u.view.Painter.Colors)
}
func (u *appServerUI) stateLabel(now time.Time) string {
	return paintMainStatus(u.mainPresentation(now).Status, now, u.view.Painter.Colors)
}
func (u *appServerUI) composerErrorText() string {
	return mainStatusDetails(u.mainPresentation(u.now()).Status)
}
