package launch

import (
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"slices"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/modules/conversation/composer"
	conversation "github.com/yusing/mekugi/modules/conversation/presentation"
)

// nativeViewEffects retains one view's semantic editor/clipboard consequences
// until that client acknowledges delivery. It never retains an editor owner.
type nativeViewEffects struct {
	next       uint64
	pending    []sessionapi.NativeEffect
	batchBytes int
	activity   []sessionapi.NativeEffect
	reset      uint64
	reserved   int
	current    *nativeEffectReservation
	nextInput  uint64
	inputs     map[uint64]bool
}

func (e *nativeViewEffects) add(effect sessionapi.NativeEffect) {
	reservation := e.current
	if reservation == nil {
		reservation = e.reserve(1)
	}
	reservation.add(effect)
}

// Batch only consecutive, unpublished deliveries. Published IDs are immutable:
// the client may already have applied them while their receipt is in flight.
// Small deltas share a bounded batch; larger deliveries retain their own slot.
func (e *nativeViewEffects) deliver(thread string, deliveries []sessionpresentation.ActivityDelivery) {
	// Selection follows roster thread identity at publication, so a skipped
	// caller rename never needs a replayable renderer transition.
	rename := func(delivery sessionpresentation.ActivityDelivery) bool { return delivery.Rename != nil }
	if slices.ContainsFunc(deliveries, rename) {
		deliveries = slices.DeleteFunc(slices.Clone(deliveries), rename)
	}
	if len(deliveries) == 0 {
		return
	}
	const maxRows, maxBytes = 64, 64 << 10
	data, err := json.Marshal(&deliveries, jsonv1.FormatDurationAsNano(true))
	if err != nil || len(data) > maxBytes {
		// Source admission has already retained the content. A large delivery
		// catches up from that publication rather than retaining another copy.
		e.next++
		e.reset, e.activity, e.batchBytes = e.next, nil, 0
		return
	}
	batchable := len(deliveries) <= maxRows
	if batchable && e.batchBytes > 0 && len(e.activity) > 0 {
		last := &e.activity[len(e.activity)-1]
		if last.Thread == thread && len(last.Deliveries)+len(deliveries) <= maxRows && e.batchBytes+len(data) <= maxBytes {
			last.Deliveries = append(last.Deliveries, deliveries...)
			e.batchBytes += len(data)
			return
		}
	}
	if len(e.activity) >= 64 {
		e.reset = e.activity[len(e.activity)-1].ID
		e.activity = nil
	}
	e.next++
	e.activity = append(e.activity, sessionapi.NativeEffect{ID: e.next, Thread: thread, Deliveries: slices.Clone(deliveries)})
	e.batchBytes = 0
	if batchable {
		e.batchBytes = len(data)
	}
}

func (e *nativeViewEffects) publication() []sessionapi.NativeEffect {
	e.batchBytes = 0
	return slices.Clone(e.pending)
}

func (e *nativeViewEffects) activityPublication() *sessionapi.NativeActivityFrame {
	e.batchBytes = 0
	if len(e.activity) == 0 && e.reset == 0 {
		return nil
	}
	return &sessionapi.NativeActivityFrame{Reset: e.reset, Effects: slices.Clone(e.activity)}
}

func (e *nativeViewEffects) acknowledge(id uint64, preserveRecovery bool) bool {
	if id > e.next {
		return false
	}
	e.pending = slices.DeleteFunc(e.pending, func(effect sessionapi.NativeEffect) bool {
		return effect.ID <= id && (!preserveRecovery || effect.RecoveryDraft() == nil)
	})
	e.activity = slices.DeleteFunc(e.activity, func(effect sessionapi.NativeEffect) bool { return effect.ID <= id })
	e.batchBytes = 0
	return true
}

func (e *nativeViewEffects) recovery(id uint64) *sessionapi.NativeEffect {
	for i := range e.pending {
		if e.pending[i].ID == id && e.pending[i].RecoveryDraft() != nil {
			return &e.pending[i]
		}
	}
	return nil
}

func (e *nativeViewEffects) discardRecovery(id uint64) {
	e.pending = slices.DeleteFunc(e.pending, func(effect sessionapi.NativeEffect) bool { return effect.ID == id })
}

// Restoration belongs to the originating editor lifetime, not the currently
// displayed client. A detached client has no editor to restore; its execution
// and existing history/failure evidence remain owned by the session.
func (n *nativeViews) restore(execution *executionPresentation, parts []conversation.Input) {
	byView := make(map[string][]conversation.Input)
	for _, part := range parts {
		byView[part.Origin] = append(byView[part.Origin], part)
	}
	for id, inputs := range byView {
		view := n.views[id]
		if view == nil || view.ctx.Err() != nil || view.attached.executionPresentation != execution {
			continue
		}
		if view.remoteHuman {
			// Remote recovery joins consecutive inputs with the same delivery mode.
			// Only a preview crosses transport until the user opens text editing.
			for len(inputs) > 0 {
				end := 1
				for end < len(inputs) && inputs[end].NextTurn == inputs[0].NextTurn {
					end++
				}
				batch := inputs[:end]
				draft := composer.Join(conversation.Drafts(batch)...)
				view.effects.restore(batch, sessionapi.NativeEffect{Thread: view.thread, Restore: &draft,
					Recovery: &sessionapi.NativeRecoveryView{Queued: batch[0].NextTurn}})
				inputs = inputs[end:]
			}
			continue
		}
		// Restore prepends. Deliver inputs in reverse order so the editor keeps
		// their original order without joining individually deliverable payloads
		// into one oversized consequence.
		for i := len(inputs) - 1; i >= 0; i-- {
			view.effects.restore(inputs[i:i+1], sessionapi.NativeEffect{Thread: view.thread, Restore: new(inputs[i].Draft)})
		}
	}
}

func (n *nativeViews) restoreShell(execution *executionPresentation, input conversation.Input) {
	view := n.views[input.Origin]
	if view == nil || view.ctx.Err() != nil || view.attached.executionPresentation != execution {
		return
	}
	view.effects.restore([]conversation.Input{input}, sessionapi.NativeEffect{Thread: view.thread, ShellRestore: new(input.Draft)})
}

func (n *nativeViews) restoreSide(execution *executionPresentation, input conversation.Input, thread string) {
	view := n.views[input.Origin]
	if view == nil || view.ctx.Err() != nil || view.attached.executionPresentation != execution {
		return
	}
	if thread == "" {
		thread = view.thread
	}
	view.effects.restore([]conversation.Input{input}, sessionapi.NativeEffect{Thread: thread, Restore: new(input.Draft)})
}

func (n *nativeViews) deliverActivity(execution *executionPresentation, deliveries []sessionpresentation.ActivityDelivery) {
	if len(deliveries) == 0 {
		return
	}
	for _, view := range n.views {
		if view.ctx.Err() == nil && view.attached.executionPresentation == execution {
			view.effects.deliver(view.thread, deliveries)
		}
	}
}

func (n *nativeViews) notify(execution *executionPresentation, event, message string) {
	for _, view := range n.views {
		if view.ctx.Err() == nil && view.attached.executionPresentation == execution {
			view.effects.next++
			// Desktop attention is replaceable; full notices/history remain owned
			// independently of the latest wake-up signal.
			view.notification = &sessionapi.NativeEffect{ID: view.effects.next, Notification: &sessionapi.NativeDesktopNotification{Event: event, Message: message}}
		}
	}
}

// Explicit queue editing transfers delivery ownership to the requesting view.
// Automatic rejection still uses restore and the admitted input's origin.
func (n *nativeViews) claimWaiting(view *nativeSessionView) {
	for _, stack := range [][]conversation.Input{view.attached.conversation.Unsent, view.attached.conversation.Queued} {
		for i := range stack {
			part := &stack[i]
			if part.Question != nil || part.Origin == view.attached.view {
				continue
			}
			if previous := n.views[part.Origin]; previous != nil && previous.effects.inputs[part.Delivery] {
				delete(previous.effects.inputs, part.Delivery)
				previous.effects.reserved--
			}
			part.Origin, part.Delivery = view.attached.view, view.effects.input()
		}
	}
}
