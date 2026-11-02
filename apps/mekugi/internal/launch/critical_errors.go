package launch

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/modules/notifications/notice"
)

// Take the ancestry/name snapshot before the notice owner locks: activity
// producers may submit degradation while holding their own lock.
func claimNativeNotices(c *requestexecution.Failures, root string, activity *sessioncontrol.Audience) *notice.Delivery {
	descendants, valid := noticeAudience(c, root, activity)
	if !valid {
		return nil
	}
	return c.Notices.Claim(root, descendants)
}

// nativeErrorsWaiting reports composer errors that a held claim would hide.
func nativeErrorsWaiting(c *requestexecution.Failures, root string, activity *sessioncontrol.Audience) bool {
	descendants, valid := noticeAudience(c, root, activity)
	return valid && c.Notices.Waiting(root, descendants, notice.Error)
}

func noticeAudience(c *requestexecution.Failures, root string, activity *sessioncontrol.Audience) (map[string]string, bool) {
	if c == nil || root == "" {
		return nil, false
	}
	if activity == nil {
		return nil, true
	}
	audience, valid := activity.Snapshot(root)
	return audience.Threads, valid
}
