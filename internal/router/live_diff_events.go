package router

import (
	"context"
	"maps"
	"slices"
	"sync"

	"github.com/yusing/mekugi/modules/changehistory/changes"

	"github.com/yusing/mekugi/internal/ui/diffview"
)

type liveDiffChange struct {
	Workspace string
	Namespace string `json:",omitzero"`
	Thread    string
	Stream    int
	ID        string
	Change    changes.Change
}

type liveDiffEvent struct {
	Kind         string
	Scope        *liveDiffScope    `json:",omitempty"`
	Changes      []liveDiffChange  `json:",omitempty"`
	Status       string            `json:",omitempty"`
	Preview      *diffview.Preview `json:",omitempty"`
	TurnRevision uint64            `json:",omitzero"`
	Resync       bool              `json:",omitzero"`
	PreviewIDs   []string          `json:",omitempty"` // Complete membership at a resync barrier.
}

type liveDiffSubscriber struct {
	events       chan liveDiffEvent
	gap          chan struct{}
	previewReady chan struct{}
	previews     []liveDiffEvent // Latest preview event per ID, including restoration provenance.
}

// The router owns this mailbox for the single in-process UI. Enqueueing never
// waits for the renderer, including at the durable publication boundary.
type liveDiffBroker struct {
	ctx               context.Context
	mu                sync.Mutex
	scope             liveDiffScope
	subscriber        *liveDiffSubscriber
	turnRevision      uint64
	turnStatus        string
	completedPreviews []diffview.Preview             // Last 16 evaluated snapshots in this turn, oldest first.
	previewTurns      map[string]liveDiffPreviewTurn // Latest native turn per thread.
	previews          []diffview.Preview             // Active calls in first-observation order, at most 16.
}

func newLiveDiffBroker(ctx context.Context) *liveDiffBroker {
	return &liveDiffBroker{
		ctx:   ctx,
		scope: liveDiffScope{Workspaces: make(map[string]map[string]bool)},
	}
}

func cloneLiveDiffScope(scope liveDiffScope) liveDiffScope {
	next := liveDiffScope{Workspaces: make(map[string]map[string]bool, len(scope.Workspaces))}
	for workspace, threads := range scope.Workspaces {
		next.Workspaces[workspace] = maps.Clone(threads)
	}
	return next
}

func (b *liveDiffBroker) scopeEventLocked() liveDiffEvent {
	scope := b.scope // setScope replaces, never mutates, the broker-owned maps.
	return liveDiffEvent{Kind: "scope", Scope: &scope}
}

func (b *liveDiffBroker) emitLocked(event liveDiffEvent) {
	sub := b.subscriber
	if sub == nil {
		return
	}
	select {
	case sub.events <- event:
	default:
		close(sub.gap)
		b.subscriber = nil
	}
}

// Preview snapshots are replaceable display state, not durable publications.
// Keep slow viewers from accumulating obsolete frames or starving edit receipts.
func (b *liveDiffBroker) emitPreviewLocked(preview diffview.Preview) {
	sub := b.subscriber
	if sub == nil {
		return
	}
	event := liveDiffEvent{Kind: "preview", Preview: &preview}
	if i := slices.IndexFunc(sub.previews, func(old liveDiffEvent) bool { return old.Preview.ID == preview.ID }); i >= 0 {
		sub.previews[i] = event
	} else {
		if len(sub.previews) >= 32 {
			close(sub.gap)
			b.subscriber = nil
			return
		}
		sub.previews = append(sub.previews, event)
	}
	select {
	case sub.previewReady <- struct{}{}:
	default:
	}
}

// Drain scope/receipt events and sample previews under the same publication
// lock. A newly observed thread's preview cannot overtake its scope expansion.
func (b *liveDiffBroker) takePreviews(sub *liveDiffSubscriber) []liveDiffEvent {
	b.mu.Lock()
	defer b.mu.Unlock()
	var events []liveDiffEvent
drain:
	for {
		select {
		case event := <-sub.events:
			events = append(events, event)
		default:
			break drain
		}
	}
	events = append(events, sub.previews...)
	sub.previews = nil
	return events
}

func (b *liveDiffBroker) setScope(scope liveDiffScope) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.scope = cloneLiveDiffScope(scope)
	b.emitLocked(b.scopeEventLocked())
}

// resync replaces the scope and detaches the subscriber through its gap, as
// on overflow. Resubscription restores display state from a fresh snapshot;
// previews outside the new scope stay out of the view.
func (b *liveDiffBroker) resync(scope liveDiffScope) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.scope = cloneLiveDiffScope(scope)
	if b.subscriber != nil {
		close(b.subscriber.gap)
		b.subscriber = nil
	}
}

func (b *liveDiffBroker) publish(changes []liveDiffChange) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var included []liveDiffChange
	for _, change := range changes {
		if b.scope.Workspaces[change.Workspace][change.Thread] {
			included = append(included, change)
		}
	}
	if len(included) > 0 {
		b.emitLocked(liveDiffEvent{Kind: "change", Changes: included})
	}
}

func (b *liveDiffBroker) publishTurn(active bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	status := "completed"
	if active {
		status = "active"
	}
	if active {
		b.completedPreviews = nil
	}
	b.turnStatus = status
	b.turnRevision++
	b.emitLocked(liveDiffEvent{Kind: "turn", Status: status, TurnRevision: b.turnRevision})
}

// unsubscribe only detaches the current UI mailbox; delayed cleanup from an
// earlier subscription must not detach its replacement after resynchronization.
func (b *liveDiffBroker) unsubscribe(sub *liveDiffSubscriber) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.subscriber == sub {
		b.subscriber = nil
	}
}

// subscribe replaces the UI mailbox and restores the current display state.
func (b *liveDiffBroker) subscribe() *liveDiffSubscriber {
	b.mu.Lock()
	defer b.mu.Unlock()
	sub := &liveDiffSubscriber{events: make(chan liveDiffEvent, 32), gap: make(chan struct{}), previewReady: make(chan struct{}, 1)}
	// Register before exposing the initial scope. The client may take its
	// durable snapshot while subsequent events accumulate in this queue.
	b.subscriber = sub
	event := b.scopeEventLocked()
	event.Resync = true
	for _, previews := range [][]diffview.Preview{b.completedPreviews, b.previews} {
		for _, preview := range previews {
			if b.scope.Workspaces[preview.Workspace][preview.Thread] {
				event.PreviewIDs = append(event.PreviewIDs, preview.ID)
				sub.previews = append(sub.previews, liveDiffEvent{Kind: "preview", Preview: &preview, Resync: true})
			}
		}
	}
	sub.events <- event
	if b.turnStatus != "" {
		sub.events <- liveDiffEvent{Kind: "turn", Status: b.turnStatus, TurnRevision: b.turnRevision}
	}
	if len(sub.previews) > 0 {
		sub.previewReady <- struct{}{}
	}
	return sub
}

// previewLocked reads bounded active state without changing observation order.
func (b *liveDiffBroker) previewLocked(id string) (diffview.Preview, bool) {
	for _, preview := range b.previews {
		if preview.ID == id {
			return preview, true
		}
	}
	return diffview.Preview{}, false
}

func (b *liveDiffBroker) removePreviewLocked(id string) bool {
	i := slices.IndexFunc(b.previews, func(preview diffview.Preview) bool { return preview.ID == id })
	if i < 0 {
		return false
	}
	b.previews = slices.Delete(b.previews, i, i+1)
	return true
}
