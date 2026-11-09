package launch

import (
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
)

func releaseCommand(attached *attachedPresentation, at time.Time, entry sessionpresentation.ActivityEntry) {
	if entry.Native != nil {
		attached.hooks.deliverActivity(attached.executionPresentation, []sessionpresentation.ActivityDelivery{{Command: &sessionpresentation.CommandDelivery{Kind: sessionpresentation.CommandRelease, Key: [3]string{entry.Native.Thread, entry.Native.Turn, entry.Native.Item}, MainSequence: attached.source.EntrySeq(entry), ChildSequence: attached.childSource.EntrySeq(entry), At: at}}})
	}
}
