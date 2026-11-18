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
	if facts == nil && item.Type == "agent_message" {
		task := nativeMailTask(attached, thread)
		if entry, ok := sessionpresentation.LocalNativeMail(thread, task, item, at, false); ok {
			entry.Seq = attached.session.Activity().NextSequence()
			sessionpresentation.ApplyActivity(thread, attached.source, attached.childSource, []sessionpresentation.ActivityEntry{entry}, nil)
		}
		return
	}
	entries := sessionpresentation.NativeCoordinationEntries(thread, turn, item, facts, at, false)
	for i := range entries {
		entries[i].Seq = attached.session.Activity().NextSequence()
	}
	sessionpresentation.ApplyActivity(thread, attached.source, attached.childSource, entries, nil)
}

func nativeMailTask(attached *attachedPresentation, thread string) string {
	if input := attached.threadInput; input != nil && input.admitted && input.caller != "" {
		return input.caller
	}
	return attached.session.KnownCaller(thread)
}
