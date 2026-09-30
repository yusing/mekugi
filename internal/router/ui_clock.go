package router

import "time"

func (u *appServerUI) now() time.Time {
	if u != nil && u.clock != nil {
		return u.clock()
	}
	return time.Now()
}

func (v *liveActivityView) now() time.Time {
	if v.clock != nil {
		return v.clock()
	}
	return time.Now()
}
