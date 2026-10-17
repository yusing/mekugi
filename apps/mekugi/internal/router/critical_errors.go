package router

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/modules/notifications/notice"
)

// Take the ancestry/name snapshot before the notice owner locks: activity
// producers may submit degradation while holding their own lock.
func claimNativeNotices(c *requestexecution.Failures, root string, activity *sessioncontrol.Audience) *notice.Delivery {
	if c == nil || root == "" {
		return nil
	}
	var descendants map[string]string
	if activity != nil {
		audience, valid := activity.Snapshot(root)
		if !valid {
			return nil
		}
		descendants = audience.Threads
	}
	return c.Notices.Claim(root, descendants)
}
