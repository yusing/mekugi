package launch

import (
	"slices"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

// Reuse the runtime's completion staging while a selected wait's host delivery
// settles. Ordinary messages and answers outside that subscription stay immediate.
func stageNativeDelivery(attached *attachedPresentation, input control.NativeInput, prepared sessioncontrol.PreparedCompletion, now time.Time) {
	p := attached.threadInput
	var waitID string
	if input.Kind == "FINAL_ANSWER" && p != nil && p.driver != nil {
		if wait := p.driver.Coordination().WaitPublication(input.Thread); wait != nil && slices.Contains(wait.Targets, input.Sender) {
			waitID = wait.ID
		}
	}
	execution := attached.executionPresentation
	publish := func() bool {
		if execution != attached.executionPresentation {
			return true
		}
		if waitID != "" {
			if wait := p.driver.Coordination().WaitPublication(input.Thread); wait != nil && wait.ID == waitID && !wait.Settled() {
				return false
			}
		}
		publishNativeDelivery(attached, input, now)
		if len(prepared.Reload) != 0 {
			entry := sessionpresentation.ActivityEntry{Seq: attached.session.Activity().NextSequence(), Agent: "/root", Kind: "tool", Observed: now, Completed: now,
				Native: &sessionpresentation.NativeItem{Thread: input.Thread, Item: prepared.Source, Phase: "item/completed", Command: prepared.Reload[0].Command, Reload: prepared.Reload}}
			sessionpresentation.ApplyActivity(attached.source, attached.childSource, []sessionpresentation.ActivityEntry{entry}, nil)
		}
		return true
	}
	if publish() {
		return
	}
	p.completions = append(p.completions, logicalCompletion{publish: publish})
}

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
	}
	if input.Kind == "MESSAGE" || input.Kind == "FINAL_ANSWER" {
		entry.Message = &sessionpresentation.ActivityMessage{From: input.Sender, To: recipient, Text: input.Message}
	}
	return entry
}
