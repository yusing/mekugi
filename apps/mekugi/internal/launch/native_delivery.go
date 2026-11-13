package launch

import (
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/backends/codex/appserver"
)

// A semantic event without a response item uses the same projection and item
// identity as retained replay; Activity owns deduplication and ordering.
func publishNativeCoordination(attached *attachedPresentation, thread, turn string, item sessioncontrol.NativeMessage, facts *appserver.CoordinationProjection, at time.Time) {
	entries := sessionpresentation.NativeCoordinationEntries(thread, turn, item, facts, at, false)
	for i := range entries {
		entries[i].Seq = attached.session.Activity().NextSequence()
	}
	sessionpresentation.ApplyActivity(attached.source, attached.childSource, entries, nil)
}
