package sessionreview

import (
	"context"
	"errors"
	"slices"
)

type Result struct {
	attachment, generation uint64
	state                  Projection
	err                    error
}

// The application owns admission and the bounded backlog. Each job owns a detached
// copy, including its selected retained scope, and publishes only a complete projection.
// Slots belong to the invocation, not the attachment: cancellation cannot free a
// slot while an uncancelable filesystem call still occupies its worker.
// Source: apps/mekugi/internal/router/saved_review_loader.go:11:148@[bab31cbe] savedReviewLoader.
type Loader struct {
	ctx                    context.Context
	cancel                 context.CancelFunc
	attachment, generation uint64
	state                  Projection
	pending                []Event
	active, resync         bool
	slots                  chan struct{}
	results                chan Result
}

func NewLoader(ctx context.Context, state Projection) *Loader {
	if state.Retained.Store == nil {
		return nil
	}
	l := &Loader{slots: make(chan struct{}, 2), results: make(chan Result, 2)}
	l.Replace(ctx, state)
	return l
}

func (l *Loader) Replace(ctx context.Context, state Projection) {
	if l == nil {
		return
	}
	l.Close()
	l.ctx, l.cancel = context.WithCancel(ctx)
	l.attachment++
	l.generation++
	l.state, l.pending, l.active, l.resync = state, nil, false, false
}

func (l *Loader) Close() {
	if l != nil && l.cancel != nil {
		l.cancel()
	}
}

func (l *Loader) Updates() <-chan Result {
	if l == nil {
		return nil
	}
	return l.results
}

func (l *Loader) AwaitResync() {
	if l != nil {
		l.generation++
		l.pending, l.resync = nil, true
	}
}

func (l *Loader) Enqueue(event Event) error {
	if err := validateEvent(event); err != nil {
		return err
	}
	if event.Kind != "scope" && event.Kind != "change" {
		return nil
	}
	if event.Kind == "scope" && event.Resync {
		l.generation++
		l.pending, l.resync = nil, false
	} else if l.resync {
		return nil
	}
	if len(l.pending) == 32 {
		// Reuse the subscription's durable resync barrier rather than dropping
		// a change or accumulating unbounded history behind slow storage.
		l.AwaitResync()
		return nil
	}
	if event.Scope != nil {
		scope := CloneScope(*event.Scope)
		event.Scope = &scope
	}
	event.Changes = slices.Clone(event.Changes)
	for i := range event.Changes {
		event.Changes[i].Calls = slices.Clone(event.Changes[i].Calls)
	}
	l.pending = append(l.pending, event)
	return l.start()
}

func (l *Loader) start() error {
	if l.active || len(l.pending) == 0 {
		return nil
	}
	select {
	case l.slots <- struct{}{}:
	default:
		l.pending = nil
		return errors.New("saved Diff read capacity exhausted; use mchanges or restart the session")
	}
	l.active = true
	ctx, events := l.ctx, l.pending
	l.pending = nil
	result := Result{attachment: l.attachment, generation: l.generation, state: l.state}
	go func() {
		_, result.err = result.state.Apply(ctx, events...)

		<-l.slots
		select {
		case l.results <- result:
		case <-ctx.Done():
		}
	}()
	return nil
}

func (l *Loader) Accept(result Result) (bool, error) {
	if result.attachment != l.attachment {
		return false, nil
	}
	l.active = false
	if result.generation != l.generation {
		return false, l.start()
	}
	if result.err != nil {
		l.pending = nil
		return false, result.err
	}
	l.state = result.state
	return true, l.start()
}

// Projection returns the current complete snapshot, never an in-flight job.
func (l *Loader) Projection() Projection { return l.state }
func (l *Loader) NeedsResync() bool      { return l != nil && l.resync }
func (l *Loader) Pending() bool          { return l != nil && (l.active || len(l.pending) != 0 || l.resync) }
