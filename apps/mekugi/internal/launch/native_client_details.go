package launch

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

func (c *nativeClient) openFeedbackDetails(text string) {
	pages := activity.DetailPages(activityui.Values([]activityui.Block{{Kind: "error", Body: text}}))
	c.localDetailID = c.localDetails.OpenDetails(c.event.Frame.View.Version, nil, pages, false, nil)
	// Local and server selections have distinct owners even if their counters
	// happen to match. Force the ordinary dialog's new-selection navigation path.
	c.renderer.terminal.Dialog = nil
}

func (c *nativeClient) closeLocalDetails() bool {
	if _, ok := c.localDetails.DetailView(c.localDetailID); !ok {
		return false
	}
	c.localDetails.CloseDetails(c.localDetailID)
	c.localDetailID = sessionpresentation.DetailID{}
	c.renderer.terminal.Dialog = nil
	return true
}
