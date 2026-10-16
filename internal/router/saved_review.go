package router

import (
	"context"
	"encoding/json"
	"maps"
	"sync"

	"github.com/yusing/mekugi/contracts/scope"

	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
)

const maxReviewScopeBytes = 1 << 20

type reviewChange struct {
	Workspace   string
	Namespace   string `json:",omitzero"`
	Thread      string
	Stream      int
	ID          string
	Correlation string
	Calls       []string
}

type reviewEvent struct {
	Kind         string
	Scope        *previewmodel.Scope   `json:",omitempty"`
	Changes      []reviewChange        `json:",omitempty"`
	Status       string                `json:",omitempty"`
	Preview      *previewmodel.Preview `json:",omitempty"`
	TurnRevision uint64                `json:",omitzero"`
	Resync       bool                  `json:",omitzero"`
	PreviewIDs   []string              `json:",omitempty"`
}

type reviewSubscription struct {
	events  chan reviewEvent
	gap     chan struct{}
	preview *previewmodel.Subscription
	session *previewmodel.Session
}

// savedReview joins authorized session membership with retained change delivery.
// It is always present; preview observation is an optional consumer of its scope.
// One lock orders scope expansion and retained receipts before preview drains.
type savedReview struct {
	mu                 sync.Mutex
	preview            *previewmodel.Session
	live               *previewmodel.Service
	state              scope.Store
	ctx                context.Context
	scope              previewmodel.Scope
	workspace          string
	scopeBytes         int
	lineage, exhausted bool
	changed            chan struct{}
	notice             func(string, string)
	subscriber         *reviewSubscription
}

func newSavedReview(ctx context.Context, live *previewmodel.Service) *savedReview {
	r := &savedReview{live: live, ctx: ctx, scope: previewmodel.Scope{Workspaces: make(map[string]map[string]bool)}, scopeBytes: len(`{"Workspaces":{}}`), changed: make(chan struct{}, 1)}
	r.preview = live.Attach(ctx, &r.state)
	return r
}

func (r *savedReview) Preview() *previewmodel.Session {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.preview
}

// Close retires only this attachment's observation state. Captures, commands,
// retained publications and unrelated host work continue at their owners.
func (r *savedReview) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.preview != nil {
		r.preview.Close()
		r.preview = nil
	}
	r.state = scope.Store{}
	if r.subscriber != nil {
		close(r.subscriber.gap)
		r.subscriber = nil
	}
}

func cloneReviewScope(scope previewmodel.Scope) previewmodel.Scope {
	next := previewmodel.Scope{Workspaces: make(map[string]map[string]bool, len(scope.Workspaces))}
	for workspace, threads := range scope.Workspaces {
		next.Workspaces[workspace] = maps.Clone(threads)
	}
	return next
}

func (r *savedReview) Scope() previewmodel.Scope {
	r.mu.Lock()
	defer r.mu.Unlock()
	return cloneReviewScope(r.scope)
}
func (r *savedReview) Workspace() string        { r.mu.Lock(); defer r.mu.Unlock(); return r.workspace }
func (r *savedReview) Changed() <-chan struct{} { return r.changed }

// Observe accepts validated provider request identities, restricted to the
// selected lineage after a session switch. Restore uses authorized host metadata.
func (r *savedReview) Observe(workspace, thread, parent string, root bool) {
	if r == nil || workspace == "" || thread == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	included := func(id string) bool {
		for _, threads := range r.scope.Workspaces {
			if id != "" && threads[id] {
				return true
			}
		}
		return false
	}
	if !r.lineage || included(thread) || included(parent) {
		r.includeLocked(workspace, thread, root)
	}
}

func (r *savedReview) IncludeThread(workspace, thread string, root bool) {
	if r == nil || workspace == "" || thread == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.includeLocked(workspace, thread, root)
}

func (r *savedReview) includeLocked(workspace, thread string, root bool) {
	if r.exhausted {
		return
	}
	rootSelected := r.workspace == "" && root
	threads := r.scope.Workspaces[workspace]
	if !threads[thread] {
		workspaceJSON, _ := json.Marshal(workspace)
		threadJSON, _ := json.Marshal(thread)
		added := len(threadJSON) + len(":true")
		if len(threads) > 0 {
			added++
		}
		if threads == nil {
			added += len(workspaceJSON) + len(":{}")
			if len(r.scope.Workspaces) > 0 {
				added++
			}
		}
		if r.scopeBytes+added > maxReviewScopeBytes {
			r.exhausted = true
			r.scope = previewmodel.Scope{}
			if r.preview != nil {
				r.preview.Disable()
			}
			if r.notice != nil {
				r.notice("live_diff_scope_capacity", "Saved Diff and live previews paused because session scope exceeded 1 MiB. Edits and mchanges remain available; restart the router to reset the review scope.")
			}
		} else {
			// Every queued barrier owns immutable membership, including empty scopes.
			r.scope = cloneReviewScope(r.scope)
			threads = r.scope.Workspaces[workspace]
			if threads == nil {
				threads = make(map[string]bool)
				r.scope.Workspaces[workspace] = threads
			}
			threads[thread] = true
			r.scopeBytes += added
		}
	} else if !rootSelected {
		return
	}
	if r.preview != nil {
		r.preview.SetScope(r.scope)
	}
	scope := r.scope
	r.emitLocked(reviewEvent{Kind: "scope", Scope: &scope})
	if rootSelected && !r.exhausted {
		r.workspace = workspace
	}
	select {
	case r.changed <- struct{}{}:
	default:
	}
}

func (r *savedReview) ResetScope() {
	r.mu.Lock()
	defer r.mu.Unlock()
	enabled := r.preview.Enabled()
	if r.preview != nil {
		r.preview.Close()
	}
	r.state = scope.Store{}
	r.preview = r.live.Attach(r.ctx, &r.state)
	if enabled {
		r.preview.Enable()
	}
	r.workspace, r.lineage = "", true
	r.scope = previewmodel.Scope{Workspaces: make(map[string]map[string]bool)}
	r.scopeBytes = len(`{"Workspaces":{}}`)
	if r.preview != nil {
		r.preview.SetScope(r.scope)
	}
	if r.subscriber != nil {
		close(r.subscriber.gap)
		r.subscriber = nil
	}
}

func (r *savedReview) emitLocked(event reviewEvent) {
	if sub := r.subscriber; sub != nil {
		select {
		case sub.events <- event:
		default:
			close(sub.gap)
			r.subscriber = nil
		}
	}
}

func (r *savedReview) Publish(changes []reviewChange) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var included []reviewChange
	for _, change := range changes {
		if r.scope.Workspaces[change.Workspace][change.Thread] {
			included = append(included, change)
		}
	}
	if len(included) > 0 {
		r.emitLocked(reviewEvent{Kind: "change", Changes: included})
	}
}

func (r *savedReview) Subscribe() *reviewSubscription {
	r.mu.Lock()
	defer r.mu.Unlock()
	sub := &reviewSubscription{events: make(chan reviewEvent, 32), gap: make(chan struct{})}
	scope := r.scope
	event := reviewEvent{Kind: "scope", Scope: &scope, Resync: true}
	if r.preview != nil {
		sub.session = r.preview
		sub.preview, event.PreviewIDs = sub.session.Subscribe()
	}
	r.subscriber = sub
	sub.events <- event
	return sub
}

func (r *savedReview) Unsubscribe(sub *reviewSubscription) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.subscriber == sub {
		r.subscriber = nil
	}
	if sub.session != nil {
		sub.session.Unsubscribe(sub.preview)
	}
}

func (r *savedReview) TakePreviews(sub *reviewSubscription) []reviewEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.subscriber != sub {
		return nil
	}
	var events []reviewEvent
drain:
	for {
		select {
		case event := <-sub.events:
			events = append(events, event)
		default:
			break drain
		}
	}
	if sub.session != nil {
		for _, event := range sub.session.TakePreviews(sub.preview) {
			events = append(events, reviewEvent{Kind: event.Kind, Preview: event.Preview, Status: event.Status, TurnRevision: event.TurnRevision, Resync: event.Resync})
		}
	}
	return events
}

func (s *reviewSubscription) Events() <-chan reviewEvent { return s.events }
func (s *reviewSubscription) Gap() <-chan struct{}       { return s.gap }
func (s *reviewSubscription) Ready() <-chan struct{} {
	if s.preview != nil {
		return s.preview.Ready()
	}
	return nil
}
func (s *reviewSubscription) PreviewGap() <-chan struct{} {
	if s.preview != nil {
		return s.preview.Gap()
	}
	return nil
}
