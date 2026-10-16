package preview

import (
	"context"
	"maps"
	"slices"
	"sync"
	"sync/atomic"
)

type Event struct {
	Kind         string
	Status       string   `json:",omitempty"`
	Preview      *Preview `json:",omitempty"`
	TurnRevision uint64   `json:",omitzero"`
	Resync       bool     `json:",omitzero"`
}

type Subscription struct {
	events       chan Event
	gap          chan struct{}
	previewReady chan struct{}
	previews     []Event // Latest preview event per ID, including restoration provenance.
}

// Session owns bounded provisional snapshots and their replaceable mailbox.
// The application supplies admitted scope and delivers retained changes separately.
type Session struct {
	ctx               context.Context
	cancel            context.CancelFunc
	enabled           atomic.Bool
	turn              rootTurn
	turnComplete      bool
	mu                sync.Mutex
	scope             Scope
	subscriber        *Subscription
	turnRevision      uint64
	turnStatus        string
	completedPreviews []Preview              // Last 16 evaluated snapshots in this turn, oldest first.
	previewTurns      map[string]previewTurn // Latest native turn per thread.
	previews          []Preview              // Active calls in first-observation order, at most 16.
}

func cloneScope(scope Scope) Scope {
	next := Scope{Workspaces: make(map[string]map[string]bool, len(scope.Workspaces))}
	for workspace, threads := range scope.Workspaces {
		next.Workspaces[workspace] = maps.Clone(threads)
	}
	return next
}

func (b *Session) emitLocked(event Event) {
	sub := b.subscriber
	if sub == nil {
		return
	}
	select {
	case sub.events <- event:
		select {
		case sub.previewReady <- struct{}{}:
		default:
		}
	default:
		close(sub.gap)
		b.subscriber = nil
	}
}

// Preview snapshots are replaceable display state, not durable publications.
// Keep slow viewers from accumulating obsolete frames or starving edit receipts.
func (b *Session) emitPreviewLocked(preview Preview) {
	sub := b.subscriber
	if sub == nil {
		return
	}
	event := Event{Kind: "preview", Preview: &preview}
	if i := slices.IndexFunc(sub.previews, func(old Event) bool { return old.Preview.ID == preview.ID }); i >= 0 {
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

// TakePreviews samples turn events and replaceable preview frames atomically.
// The application drains its admitted scope and retained receipts first.
func (b *Session) TakePreviews(sub *Subscription) []Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	var events []Event
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

func (b *Session) publishTurnLocked(active bool) {
	status := "completed"
	if active {
		status = "active"
	}
	if active {
		b.completedPreviews = nil
	}
	b.turnStatus = status
	b.turnRevision++
	b.emitLocked(Event{Kind: "turn", Status: status, TurnRevision: b.turnRevision})
}

// Unsubscribe only detaches the current UI mailbox; delayed cleanup from an
// earlier subscription must not detach its replacement after resynchronization.
func (b *Session) Unsubscribe(sub *Subscription) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.subscriber == sub {
		b.subscriber = nil
	}
}

// Subscribe replaces the UI mailbox and atomically snapshots restored frame
// membership for the application's authoritative scope barrier.
func (b *Session) Subscribe() (*Subscription, []string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	sub := &Subscription{events: make(chan Event, 32), gap: make(chan struct{}), previewReady: make(chan struct{}, 1)}
	// Register before returning membership. Later updates accumulate while the
	// application loads retained evidence for its resync barrier.
	b.subscriber = sub
	var ids []string
	for _, previews := range [][]Preview{b.completedPreviews, b.previews} {
		for _, preview := range previews {
			if b.scope.Workspaces[preview.Workspace][preview.Thread] {
				ids = append(ids, preview.ID)
				sub.previews = append(sub.previews, Event{Kind: "preview", Preview: &preview, Resync: true})
			}
		}
	}
	if b.turnStatus != "" {
		sub.events <- Event{Kind: "turn", Status: b.turnStatus, TurnRevision: b.turnRevision}
	}
	if len(sub.events) > 0 || len(sub.previews) > 0 {
		sub.previewReady <- struct{}{}
	}
	return sub, ids
}

// previewLocked reads bounded active state without changing observation order.
func (b *Session) previewLocked(id string) (Preview, bool) {
	for _, preview := range b.previews {
		if preview.ID == id {
			return preview, true
		}
	}
	return Preview{}, false
}

func (b *Session) removePreviewLocked(id string) bool {
	i := slices.IndexFunc(b.previews, func(preview Preview) bool { return preview.ID == id })
	if i < 0 {
		return false
	}
	b.previews = slices.Delete(b.previews, i, i+1)
	return true
}

// Context is the attached session lifetime used by bounded observation workers.
func (b *Session) Context() context.Context { return b.ctx }

// Close cancels observation only. Host execution and retained capture have
// independent lifetimes in the application.
func (b *Session) Close() { b.enabled.Store(false); b.cancel() }

// Current returns active frames in first-observation order. The snapshots are
// immutable observation values; callers never mutate their source slices.
func (b *Session) Current() []Preview {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.previews)
}

func (s *Subscription) Events() <-chan Event   { return s.events }
func (s *Subscription) Gap() <-chan struct{}   { return s.gap }
func (s *Subscription) Ready() <-chan struct{} { return s.previewReady }
