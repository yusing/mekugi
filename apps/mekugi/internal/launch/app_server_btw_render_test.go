package launch

import (
	"strings"

	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
)

func setBTWTest(u *appServerUI, side *sessioncontrol.Side) {
	u.attached.conversation.BTW = side.SideAnswer
	u.BTW = side
}

func (u *appServerUI) sideFrame(width, height int) []string {
	screen := vt.NewEmulator(width, height)
	defer screen.Close()
	if err := u.testPaint(screen, width, height); err != nil {
		panic(err)
	}
	return strings.Split(screen.String(), "\n")
}
