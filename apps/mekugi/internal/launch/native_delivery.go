package launch

import (
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func publishNativeDelivery(attached *attachedPresentation, input control.NativeInput, now time.Time) {
	entry := nativeDeliveryEntry(input, control.MessageItemID(input.ID), now)
	entry.Seq = attached.session.Activity().NextSequence()
	sessionpresentation.ApplyActivity(attached.source, attached.childSource, []sessionpresentation.ActivityEntry{entry}, nil)
}

func nativeDeliveryEntry(input control.NativeInput, item string, now time.Time) sessionpresentation.ActivityEntry {
	entry := sessionpresentation.ActivityEntry{Agent: input.Sender, Kind: "final", Text: input.Message, Observed: now, Completed: now,
		Native: &sessionpresentation.NativeItem{Thread: input.Thread, Item: item, Phase: "item/completed"}}
	if input.Kind == "MESSAGE" {
		entry.Kind = "reply"
		entry.Message = &sessionpresentation.ActivityMessage{From: input.Sender, To: "Main", Text: input.Message}
	}
	return entry
}
