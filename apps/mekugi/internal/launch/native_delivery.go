package launch

import (
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func publishNativeDelivery(attached *attachedPresentation, input control.NativeInput, now time.Time) {
	entry := nativeDeliveryEntry(input, control.MessageItemID(input.ID), now, false)
	entry.Seq = attached.session.Activity().NextSequence()
	sessionpresentation.ApplyActivity(attached.source, attached.childSource, []sessionpresentation.ActivityEntry{entry}, nil)
}

func nativeDeliveryEntry(input control.NativeInput, item string, now time.Time, inherited bool) sessionpresentation.ActivityEntry {
	entry := sessionpresentation.ActivityEntry{Agent: input.Sender, Kind: "final", Text: input.Message, Observed: now, Completed: now,
		Native: &sessionpresentation.NativeItem{Thread: input.Thread, Item: item, Phase: "item/completed", Status: string(input.Outcome)}}
	recipient := "Main"
	if inherited {
		recipient = input.Recipient
	}
	if input.Kind == "NEW_TASK" {
		entry.Kind = "assignment"
		entry.Assignment = &sessionpresentation.ActivityAssignment{ID: item, From: input.Sender, To: recipient, Text: input.Message}
	}
	if input.Kind == "MESSAGE" {
		entry.Kind = "reply"
		entry.Message = &sessionpresentation.ActivityMessage{From: input.Sender, To: recipient, Text: input.Message}
	}
	return entry
}
