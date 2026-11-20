package sessionpresentation

import (
	"strconv"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/contracts/observation"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
)

// NativeCoordinationEntries consumes the same frozen facts in live raw events
// and retained response envelopes. Text never supplies terminal authority.
func NativeCoordinationEntries(thread, turn string, item sessioncontrol.NativeMessage, facts *observation.CoordinationProjection, at time.Time, inherited bool) []ActivityEntry {
	var entries []ActivityEntry
	if facts != nil && facts.Wait != nil && facts.Wait.CallerThreadID != thread && !inherited {
		return entries
	}
	// A wait claim may carry several finals without any agent_message item.
	// The frozen delivery owns payload, outcome and stable display identity.
	if facts != nil {
		for _, terminal := range facts.Deliveries {
			delivery := sessioncontrol.NativeDelivery{Thread: thread, ID: "native:delivery:" + terminal.DeliveryID, Sender: terminal.Source.TaskName, Recipient: terminal.Recipient, Kind: "FINAL_ANSWER", Message: terminal.Body, Outcome: terminal.Disposition}
			entry := NativeDeliveryEntry(delivery, delivery.ID, at, inherited)
			entry.Native.NativeWork = &terminal.Source
			entries = append(entries, entry)
		}
	}
	if facts != nil && facts.Wait != nil {
		for _, message := range facts.Wait.Messages {
			sender := ""
			if message.SenderTaskName != nil {
				sender = *message.SenderTaskName
			}
			delivery := sessioncontrol.NativeDelivery{Thread: thread, ID: "native:delivery:" + message.DeliveryID, Sender: sender, Recipient: message.Recipient, Kind: "MESSAGE", Message: message.Body}
			entries = append(entries, NativeDeliveryEntry(delivery, delivery.ID, at, inherited))
		}
	}
	if facts != nil && facts.Interrupt != nil {
		interrupt := facts.Interrupt
		value := interruptValue("/root", interrupt.Target.TaskName, "")
		entries = append(entries, ActivityEntry{Agent: "/root", Kind: "progress", Text: value.ProgressText(), Observed: at, Completed: at, Native: &NativeItem{Thread: thread, Turn: turn, Item: "native:interrupt:" + interrupt.OperationID, Phase: "supplement", InterruptTarget: interrupt.Target.TaskName, InterruptID: interrupt.OperationID, NativeWork: &interrupt.Target, Wait: value}})
	}
	if delivery, ok := item.Delivery(); ok && (facts == nil || len(facts.Deliveries) == 0 && (facts.Wait == nil || len(facts.Wait.Messages) == 0)) {
		delivery.Thread = thread
		entries = append(entries, NativeDeliveryEntry(delivery, item.ID, at, inherited))
	}
	if facts == nil || facts.Wait == nil {
		return entries
	}
	wait := facts.Wait
	if wait.WaitID == "" || wait.CallerThreadID != thread && !inherited {
		return entries
	}
	// Provider retry continuations are not descendant wait-completion rows.
	if wait.Origin.Kind == "turn" && (wait.Origin.Reason == "providerCapacity" || wait.Origin.Reason == "providerUsageLimit") {
		return entries
	}
	value := &activity.Value{Kind: "progress", Body: "Finished waiting for"}
	for _, target := range wait.Selected {
		value.WaitTargets = append(value.WaitTargets, activity.WaitTarget{Name: target.TaskName})
	}
	switch wait.Result.Kind {
	case "released":
		switch wait.Result.Wake {
		case "deadline":
			value.WaitReason = "timeout"
		case "progress":
			value.WaitReason = "yielded"
		case "events":
			value.WaitReason = "completed"
			if len(wait.Messages) > 0 {
				value.WaitReason = "message received"
				value.WaitTargets = nil
				for _, message := range wait.Messages {
					if message.SenderTaskName != nil {
						value.WaitTargets = append(value.WaitTargets, activity.WaitTarget{Name: *message.SenderTaskName})
					}
				}
			} else if len(wait.Outcomes) > 0 {
				value.WaitTargets = nil
				for _, outcome := range wait.Outcomes {
					value.WaitTargets = append(value.WaitTargets, activity.WaitTarget{Name: outcome.Target.TaskName, Status: outcome.Disposition})
				}
			}
		default:
			return entries
		}
	case "cancelled":
		if wait.Result.Cause == nil {
			return entries
		}
		value.WaitReason = "canceled"
		switch wait.Result.Cause.Kind {
		case "shellOutput":
			value.WaitReason = "shell completed"
		case "executionFailed":
			value.WaitReason = "failed"
		case "dispatchRejected", "notDispatched":
			value.WaitReason = "delivery failed"
		}
	default:
		return entries
	}
	entry := ActivityEntry{Agent: "/root", Kind: "progress", Text: value.ProgressText(), Observed: at, Completed: at,
		Native: &NativeItem{Thread: thread, Turn: wait.Origin.TurnID, Item: "native:wait:" + wait.WaitID, Phase: "supplement", Wait: value, NativeOutcomes: wait.Outcomes}}
	entries = append(entries, entry)
	return entries
}

func NativeDeliveryEntry(input sessioncontrol.NativeDelivery, item string, at time.Time, inherited bool) ActivityEntry {
	entry := ActivityEntry{Agent: input.Sender, Kind: "final", Text: input.Message, Observed: at, Completed: at,
		Native: &NativeItem{Thread: input.Thread, Item: item, Phase: "item/completed", Status: input.Outcome}}
	recipient := "Main"
	if inherited {
		recipient = input.Recipient
	}
	switch input.Kind {
	case "NEW_TASK":
		entry.Kind = "assignment"
		entry.Assignment = &ActivityAssignment{ID: item, From: input.Sender, To: recipient, Text: input.Message}
	case "MESSAGE":
		entry.Kind = "reply"
	}
	if input.Kind == "MESSAGE" || input.Kind == "FINAL_ANSWER" {
		entry.Message = &ActivityMessage{From: input.Sender, To: recipient, Text: input.Message}
	}
	return entry
}

// LocalNativeMail projects a native item only onto an endpoint that owns one
// end of the message. The local transcript labels that participant Main.
func LocalNativeMail(thread, task string, item sessioncontrol.NativeMessage, at time.Time, inherited bool) (ActivityEntry, bool) {
	input, ok := item.Delivery()
	outbound := !inherited && task == input.Sender && (input.Kind == "MESSAGE" || input.Kind == "NEW_TASK")
	if !ok || !inherited && task != input.Recipient && !outbound {
		return ActivityEntry{}, false
	}
	input.Thread = thread
	entry := NativeDeliveryEntry(input, item.ID, at, inherited)
	if outbound {
		if entry.Assignment != nil {
			entry.Assignment.From, entry.Assignment.To = "Main", input.Recipient
		} else {
			entry.Message.From, entry.Message.To = "Main", input.Recipient
		}
	}
	return entry, true
}

// NativeContextResetEntry paints the actual reset receipt, independently of any
// wait cancellation. Ordinary success is already the native compaction item.
func NativeContextResetEntry(reset sessioncontrol.ContextReset, at time.Time) (ActivityEntry, bool) {
	text, kind := "", "progress"
	switch reset.Status {
	case "failed":
		text, kind = "Compaction not performed", "error"
	case "canceled":
		text = "Compaction canceled"
	case "completed":
		if reset.Diagnostic == "" {
			return ActivityEntry{}, false
		}
		text = "Compaction completed"
	default:
		return ActivityEntry{}, false
	}
	if reset.Diagnostic != "" {
		text += ": " + reset.Diagnostic
	}
	id := reset.ResetID
	if id == "" && reset.RequestID != "" {
		id = reset.RequestID
	}
	if id == "" {
		id = strconv.FormatUint(reset.Generation, 10)
	}
	return ActivityEntry{Agent: "/root", Kind: kind, Text: text, Observed: at, Completed: at, Native: &NativeItem{Thread: reset.Thread, Turn: reset.Turn, Item: "native:reset:" + id, Phase: "item/completed", Status: reset.Status}}, true
}

// interruptValue names the target as an agent, so renderers show its display
// name in its own color rather than its task path.
func interruptValue(from, target, status string) *activity.Value {
	value := &activity.Value{Kind: "progress", From: from, Body: "Interrupt requested for", WaitTargets: []activity.WaitTarget{{Name: target}}, WaitReason: status}
	if status == "interrupted" {
		value.Body, value.WaitReason = "Interrupted", ""
	}
	return value
}

// settleNativeInterrupt joins frozen facts already in this bounded Activity feed.
// It neither observes mutable work nor treats an acknowledgement as completion.
func (v *ActivitySource) settleNativeInterrupt(entry *ActivityEntry) {
	n := entry.Native
	if n == nil || n.InterruptID == "" && n.NativeWork == nil && len(n.NativeOutcomes) == 0 {
		return
	}
	settle := func(request *ActivityEntry, terminal ActivityEntry) {
		target := request.Native.NativeWork
		if target == nil || request.Native.InterruptID == "" || terminal.Native == nil {
			return
		}
		status := ""
		source := terminal.Native.NativeWork
		if terminal.Kind == "final" && source != nil && target.ThreadID == source.ThreadID && target.WorkID == source.WorkID {
			status = terminal.Native.Status
		}
		for _, outcome := range terminal.Native.NativeOutcomes {
			if target.ThreadID == outcome.Target.ThreadID && target.WorkID == outcome.Target.WorkID {
				status = outcome.Disposition
			}
		}
		if status != "interrupted" && status != "completed" && status != "failed" {
			return
		}
		native := *request.Native
		request.Native = &native
		value := interruptValue(request.Agent, target.TaskName, status)
		request.Text, request.Native.Wait = value.ProgressText(), value
	}
	if n.InterruptID != "" {
		for _, previous := range v.Entries {
			settle(entry, previous.Source)
		}
		return
	}
	if entry.Kind != "final" && len(n.NativeOutcomes) == 0 {
		return
	}
	for i, previous := range v.Entries {
		if previous.Source.Native == nil || previous.Source.Native.NativeWork == nil || previous.Source.Native.InterruptID == "" {
			continue
		}
		request := previous.Source
		settle(&request, *entry)
		if request.Text != previous.Source.Text {
			v.Model.Replace(i, request, ActivityContent(request).Values())
		}
	}
}
