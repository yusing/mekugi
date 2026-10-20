package launch

import (
	"strings"

	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
)

func setBTWTest(u *appServerUI, side *sessioncontrol.Side) {
	u.attached.conversation.BTW = side.SideAnswer
	u.BTW = side
}

func (u *appServerUI) btwRows(width, height int) []string {
	settlePresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, u.clock, &u.questions, &u.approvals, u.statusPanel != nil || u.resumePicker != nil)
	if height < 1 {
		return nil
	}
	return u.shell.Main.BTW.Render(&u.shell.Transcript.Painter, presentationTime(u.clock), width, height).Rows
}

func (u *appServerUI) sideFrame(width, height int) []string {
	screen := vt.NewEmulator(width, height)
	defer screen.Close()
	if err := u.testPaint(screen, width, height); err != nil {
		panic(err)
	}
	return strings.Split(screen.String(), "\n")
}
