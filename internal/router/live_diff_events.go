package router

import (
	"cmp"
	"context"
	"maps"
	"slices"
	"sync"
)

const maxLiveDiffEventBytes = 1 << 20

type liveDiffChange struct {
	Workspace string
	Namespace string `json:",omitzero"`
	Thread    string
	Stream    int
	ID        string
	Change    trackedChange
}

type liveDiffEvent struct {
	Kind         string
	Scope        *liveDiffScope   `json:",omitempty"`
	Changes      []liveDiffChange `json:",omitempty"`
	Status       string           `json:",omitempty"`
	Preview      *liveDiffPreview `json:",omitempty"`
	TurnRevision uint64           `json:",omitzero"`
	Resync       bool             `json:",omitzero"`
}

type liveDiffSubscriber struct {
	events       chan liveDiffEvent
	gap          chan struct{}
	previewReady chan struct{}
	previews     []liveDiffPreview // Latest per ID, guarded by the broker mutex.
}

// The router owns this hub. Enqueueing never waits for a renderer or performs
// network I/O, including when called at the durable publication boundary.
type liveDiffBroker struct {
	ctx               context.Context
	mu                sync.Mutex
	scope             liveDiffScope
	subs              map[*liveDiffSubscriber]bool
	turnRevision      uint64
	turnStatus        string
	completedPreviews []liveDiffPreview // Last 16 evaluated snapshots in this turn, oldest first.
	previews          map[string]liveDiffPreview
}

func newLiveDiffBroker(ctx context.Context) *liveDiffBroker {
	return &liveDiffBroker{
		ctx:   ctx,
		scope: liveDiffScope{Workspaces: make(map[string]map[string]bool)},
		subs:  make(map[*liveDiffSubscriber]bool),
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
	for sub := range b.subs {
		select {
		case sub.events <- event:
		default:
			close(sub.gap)
			delete(b.subs, sub)
		}
	}
}

// Preview snapshots are replaceable display state, not durable publications.
// Keep slow viewers from accumulating obsolete frames or starving edit receipts.
func (b *liveDiffBroker) emitPreviewLocked(preview liveDiffPreview) {
	for sub := range b.subs {
		if i := slices.IndexFunc(sub.previews, func(old liveDiffPreview) bool { return old.ID == preview.ID }); i >= 0 {
			sub.previews = slices.Delete(sub.previews, i, i+1)
		}
		if len(sub.previews) >= 32 {
			close(sub.gap)
			delete(b.subs, sub)
			continue
		}
		sub.previews = append(sub.previews, preview)
		select {
		case sub.previewReady <- struct{}{}:
		default:
		}
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
	for _, preview := range sub.previews {
		events = append(events, liveDiffEvent{Kind: "preview", Preview: &preview})
	}
	sub.previews = nil
	return events
}

func (b *liveDiffBroker) setScope(scope liveDiffScope) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.scope = cloneLiveDiffScope(scope)
	b.emitLocked(b.scopeEventLocked())
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

func (b *liveDiffBroker) subscribe() *liveDiffSubscriber {
	b.mu.Lock()
	defer b.mu.Unlock()
	sub := &liveDiffSubscriber{events: make(chan liveDiffEvent, 32), gap: make(chan struct{}), previewReady: make(chan struct{}, 1)}
	// Register before exposing the initial scope. The client may take its
	// durable snapshot while subsequent events accumulate in this queue.
	b.subs[sub] = true
	event := b.scopeEventLocked()
	event.Resync = true
	sub.events <- event
	if b.turnStatus != "" {
		sub.events <- liveDiffEvent{Kind: "turn", Status: b.turnStatus, TurnRevision: b.turnRevision}
	}
	sub.previews = append(sub.previews, b.completedPreviews...)
	for _, preview := range b.previews {
		sub.previews = append(sub.previews, preview)
	}
	if len(sub.previews) > 0 {
		sub.previewReady <- struct{}{}
	}
	return sub
}

// notifyLiveDiff runs only after a successful atomic index publication. Events
// carry one committed batch, not an instruction to rescan the whole store.
func (s *mekugiReplayStore) notifyLiveDiff(index changeIndex, updates map[string][]trackedCall) {
	if s.liveDiff == nil {
		return
	}
	var changes []liveDiffChange
	for id, calls := range updates {
		streamName, _, err := parseChangeID(id)
		if err != nil {
			continue // The durable index validator already owns this check.
		}
		for stream, info := range index.Streams {
			if changeStreamName(stream) == streamName {
				byThread := make(map[string][]trackedCall)
				for _, call := range calls {
					thread := cmp.Or(call.Thread, info.Thread)
					byThread[thread] = append(byThread[thread], call)
				}
				for _, thread := range slices.Sorted(maps.Keys(byThread)) {
					changes = append(changes, liveDiffChange{
						Workspace: index.Workspace, Namespace: index.Namespace, Thread: thread, Stream: stream, ID: id,
						Change: trackedChange{Correlation: index.Changes[id].Correlation, Calls: byThread[thread]},
					})
				}
				break
			}
		}
	}
	s.liveDiff(changes)
}
