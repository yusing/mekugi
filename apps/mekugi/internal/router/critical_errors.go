package router

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"
	"github.com/yusing/mekugi/modules/notifications/notice"
)

// Take the ancestry/name snapshot before the notice owner locks: activity
// producers may submit degradation while holding their own lock.
func claimNativeNotices(c *requestexecution.Failures, root string, activity *subagentActivity) *notice.Delivery {
	if c == nil || root == "" {
		return nil
	}
	var descendants map[string]string
	if activity != nil {
		activity.mu.Lock()
		audience, valid := activity.audienceLocked(root)
		if !valid {
			activity.mu.Unlock()
			return nil
		}
		descendants = audience.Threads
		activity.mu.Unlock()
	}
	return c.Notices.Claim(root, descendants)
}
