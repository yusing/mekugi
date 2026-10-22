package launch

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	conversation "github.com/yusing/mekugi/modules/conversation/presentation"
)

const nativeSemanticEffects = 64

// A reservation belongs to the admitted operation until its consequences have
// been retained or its outcome makes restoration unnecessary. Asynchronous work
// must carry this reservation through settlement, not release it after dispatch.
type nativeEffectReservation struct {
	owner     *nativeViewEffects
	remaining int
}

func (e *nativeViewEffects) reserve(count int) *nativeEffectReservation {
	if count < 1 || count > nativeSemanticEffects-len(e.pending)-e.reserved {
		return nil
	}
	e.reserved += count
	return &nativeEffectReservation{owner: e, remaining: count}
}

// input transfers one admission slot to provenance carried by the actual input
// owners. The reservation map contains no draft or host-request duplicate.
func (e *nativeViewEffects) input() uint64 {
	if e == nil {
		return 0
	}
	r := e.current
	if r == nil {
		r = e.reserve(1)
	}
	if r == nil || r.remaining == 0 {
		panic("native input requires a restoration reservation")
	}
	r.remaining--
	e.nextInput++
	if e.inputs == nil {
		e.inputs = make(map[uint64]bool)
	}
	e.inputs[e.nextInput] = true
	return e.nextInput
}

func (e *nativeViewEffects) restore(inputs []conversation.Input, effect sessionapi.NativeEffect) {
	count := 0
	for _, input := range inputs {
		if e.inputs[input.Delivery] {
			delete(e.inputs, input.Delivery)
			count++
		}
	}
	if count == 0 {
		e.add(effect)
		return
	}
	// Joined drafts need one consequence while releasing the other inputs' slots.
	r := &nativeEffectReservation{owner: e, remaining: count}
	r.add(effect)
	r.release()
}

func (n *nativeViews) reconcileEffects(view *nativeSessionView) {
	if len(view.effects.inputs) == 0 {
		return
	}
	live := make(map[uint64]bool)
	keep := func(input conversation.Input) {
		if input.Origin == view.attached.view && input.Delivery != 0 {
			live[input.Delivery] = true
		}
	}
	var visit func(*conversation.Session)
	visit = func(session *conversation.Session) {
		if session == nil {
			return
		}
		for _, input := range session.Unsent {
			keep(input)
		}
		for _, input := range session.Queued {
			keep(input)
		}
		for _, submission := range session.Submitted {
			for _, input := range submission.Parts {
				keep(input)
			}
		}
		keep(session.Shell)
		for _, side := range []*conversation.SideAnswer{session.Side, session.BTW} {
			if side != nil {
				keep(side.Pending)
				visit(&side.Drafts)
			}
		}
	}
	visit(view.attached.conversation)
	for _, input := range n.requests.PendingSideInputs() {
		keep(input)
	}
	if view.picker.sideTask != nil {
		live[view.picker.sideTaskDelivery] = true
	}
	for id := range view.effects.inputs {
		if !live[id] {
			delete(view.effects.inputs, id)
			view.effects.reserved--
		}
	}
}

func (r *nativeEffectReservation) add(effect sessionapi.NativeEffect) {
	if r == nil || r.remaining == 0 {
		panic("native effect requires an admission reservation")
	}
	r.remaining--
	r.owner.reserved--
	r.owner.next++
	effect.ID = r.owner.next
	r.owner.pending = append(r.owner.pending, effect)
	r.owner.batchBytes = 0
}

func (r *nativeEffectReservation) release() {
	if r != nil {
		r.owner.reserved -= r.remaining
		r.remaining = 0
	}
}
