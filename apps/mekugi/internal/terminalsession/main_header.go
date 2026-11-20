package terminalsession

import (
	"fmt"
	"strings"

	"github.com/yusing/mekugi/libraries/terminalui"
	"github.com/yusing/mekugi/libraries/textsanitize"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

func (in FrameInput) rosterThreadLabel() string {
	v := in.Main.session
	if v.Displayed.ID != v.Main.ID {
		if v.Displayed == v.SideThread && v.Side != nil {
			return v.Side.Label
		}
		return in.Agents.Painter.Agent(v.Displayed.Caller)
	}
	if v.Info.Caller != "" && v.Info.Caller != "/root" {
		return in.Agents.Painter.Agent(v.Info.Caller)
	}
	if in.Shell.Focus != 0 && v.Questions.Count > 0 {
		return fmt.Sprintf("Main ?%d", v.Questions.Count)
	}
	return "Main"
}

func sessionTitle(title string) string {
	return strings.Join(strings.Fields(textsanitize.Safe(title, false)), " ")
}

// scrollLabel shows how much of the feed lies above the viewport and, once
// the reader scrolls back, how much lies below and what arrived since.
func scrollLabel(v *activityui.View) string {
	below := v.FeedLines - v.Offset - v.FeedRows
	switch {
	case !v.Following && below > 0:
		label := fmt.Sprintf("▼ %d", below)
		if v.Unseen > 0 {
			label += fmt.Sprintf(" · %d new", v.Unseen)
		}
		return v.Painter.Theme.Warning() + label + terminalui.Reset
	case v.Offset > 0:
		return terminalui.Dim + fmt.Sprintf("▲ %d", v.Offset) + terminalui.Undim
	}
	return ""
}
