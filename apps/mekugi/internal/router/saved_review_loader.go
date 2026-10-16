package router

import (
	"context"
	"errors"
	"maps"
	"slices"

	"github.com/yusing/mekugi/modules/changehistory/presentation"
)

type savedReviewResult struct {
	attachment, generation uint64
	state                  savedDiff
	files                  []presentation.File
	err                    error
}

// The terminal owns admission and the bounded backlog. Each job owns a detached
// copy, including its store identity, and publishes only a complete projection.
// Slots belong to the invocation, not the attachment: cancellation cannot free a
// slot while an uncancelable filesystem call still occupies its worker.
type savedReviewLoader struct {
	ctx                    context.Context
	cancel                 context.CancelFunc
	attachment, generation uint64
	state                  savedDiff
	pending                []reviewEvent
	active, resync         bool
	slots                  chan struct{}
	results                chan savedReviewResult
}

func newSavedReviewLoader(ctx context.Context, state savedDiff) *savedReviewLoader {
	if state.store == nil {
		return nil
	}
	l := &savedReviewLoader{slots: make(chan struct{}, 2), results: make(chan savedReviewResult, 2)}
	l.replace(ctx, state)
	return l
}

func (l *savedReviewLoader) replace(ctx context.Context, state savedDiff) {
	if l == nil {
		return
	}
	l.close()
	l.ctx, l.cancel = context.WithCancel(ctx)
	l.attachment++
	l.generation++
	l.state, l.pending, l.active, l.resync = state, nil, false, false
}

func (l *savedReviewLoader) close() {
	if l != nil && l.cancel != nil {
		l.cancel()
	}
}

func (l *savedReviewLoader) updates() <-chan savedReviewResult {
	if l == nil {
		return nil
	}
	return l.results
}

func (l *savedReviewLoader) awaitResync() {
	if l != nil {
		l.generation++
		l.pending, l.resync = nil, true
	}
}

func (l *savedReviewLoader) enqueue(event reviewEvent) error {
	if err := validateLiveDiffEvent(event); err != nil {
		return err
	}
	if event.Kind != "scope" && event.Kind != "change" && event.Kind != "coverage" {
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
		l.awaitResync()
		return nil
	}
	if event.Scope != nil {
		scope := cloneReviewScope(*event.Scope)
		event.Scope = &scope
	}
	event.Changes = slices.Clone(event.Changes)
	for i := range event.Changes {
		event.Changes[i].Calls = slices.Clone(event.Changes[i].Calls)
	}
	l.pending = append(l.pending, event)
	return l.start()
}

func (l *savedReviewLoader) start() error {
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
	result := savedReviewResult{attachment: l.attachment, generation: l.generation, state: l.state}
	store := *result.state.store
	result.state.store = &store
	data := *result.state.data
	data.attempts, data.order = maps.Clone(data.attempts), slices.Clone(data.order)
	result.state.data = &data
	go func() {
		for _, event := range events {
			if result.err = ctx.Err(); result.err != nil {
				break
			}
			if _, result.err = result.state.applyEvent(ctx, event); result.err != nil {
				break
			}
		}
		if result.err == nil {
			result.files = result.state.data.files()
		}
		<-l.slots
		select {
		case l.results <- result:
		case <-ctx.Done():
		}
	}()
	return nil
}

func (l *savedReviewLoader) accept(result savedReviewResult) (bool, error) {
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
	// Preserve the selected store pointer. Jobs snapshot its invocation-local
	// identity, while subsequent jobs may observe a confirmed session switch.
	result.state.store = l.state.store
	l.state = result.state
	return true, l.start()
}
