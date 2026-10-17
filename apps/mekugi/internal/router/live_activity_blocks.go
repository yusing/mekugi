package router

import (
	"time"

	"github.com/yusing/mekugi/contracts/observation"
)

func appServerDuration(item observation.Item) time.Duration {
	if item.DurationMS == nil {
		return 0
	}
	return time.Duration(*item.DurationMS) * time.Millisecond
}
