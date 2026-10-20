package launch

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
)

func setSideTest(u *appServerUI, side *sessioncontrol.Side) {
	u.attached.conversation.Side = side.SideAnswer
	u.side = side
	settlePresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, u.clock, &u.questions, &u.approvals, u.statusPanel != nil || u.resumePicker != nil)
}

func (u *appServerUI) btwRows(width, height int) []string {
	settlePresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, u.clock, &u.questions, &u.approvals, u.statusPanel != nil || u.resumePicker != nil)
	if !u.shell.Main.Side.Open() || height < 1 {
		return nil
	}
	return u.shell.Main.Side.Render(&u.shell.Transcript.Painter, presentationTime(u.clock), width, height).Rows
}
