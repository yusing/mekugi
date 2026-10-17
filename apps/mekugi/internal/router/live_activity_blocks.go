package router

import (
	"slices"
	"time"

	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/libraries/textsanitize"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
)

func appServerDuration(item observation.Item) time.Duration {
	if item.DurationMS == nil {
		return 0
	}
	return time.Duration(*item.DurationMS) * time.Millisecond
}

// instantOperations reports tool text whose every operation prints what it
// reads at once, so its output shows whole rather than rolling through.
func instantOperations(text string) bool {
	blocks := activitypresentation.Operations(textsanitize.Safe(text, false))
	return len(blocks) > 0 && !slices.ContainsFunc(blocks, func(block activitypresentation.Value) bool { return !block.Instant() })
}
