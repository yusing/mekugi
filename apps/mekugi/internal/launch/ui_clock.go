package launch

import "time"

func presentationTime(clock func() time.Time) time.Time {
	if clock != nil {
		return clock()
	}
	return time.Now()
}
