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

// scrollLabel shows, once the reader scrolls back, how much of the feed lies
// below the viewport and what arrived since. A following view shows nothing.
func scrollLabel(v *activityui.View) string {
	below := v.FeedLines - v.Offset - v.FeedRows
	if v.Following || below <= 0 {
		return ""
	}
	label := fmt.Sprintf("▼ %d below", below)
	if v.Unseen > 0 {
		label += fmt.Sprintf(" · %d new", v.Unseen)
	}
	return v.Painter.Theme.Warning() + label + terminalui.Reset
}
