package router

import "time"

func (u *appServerUI) now() time.Time {
	if u != nil && u.clock != nil {
		return u.clock()
	}
	return time.Now()
}
