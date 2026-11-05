package sessionapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"sync"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
)

const nativeViewerLease = 45 * time.Second

// NativeActivation is sent only after the client accepts the initial frame or
// completes a handoff. Subscription admission and keyboard focus are not activation.
type NativeActivation struct {
	PID      int    `json:"pid"`
	Sequence uint64 `json:"sequence"`
}

type NativeFrontendRequest struct {
	Activation uint64            `json:"activation,omitempty"`
	PID        int               `json:"pid"`
	Instance   string            `json:"instance,omitempty"`
	Viewer     string            `json:"viewer,omitempty"`
	After      string            `json:"after,omitempty"`
	Attachment string            `json:"attachment,omitempty"`
	Displayed  string            `json:"displayed,omitempty"`
	Agent      string            `json:"agent,omitempty"`
	Content    *ContentReference `json:"content,omitempty"`
	Offset     *uint64           `json:"offset,omitempty"`
}

type NativeObservation struct {
	Activation  uint64                      `json:"activation,omitempty"`
	Available   bool                        `json:"available"`
	Instance    string                      `json:"instance,omitempty"`
	Thread      string                      `json:"thread,omitempty"`
	Startup     string                      `json:"startup,omitempty"`
	Attachment  string                      `json:"attachment,omitempty"`
	Version     sessionpresentation.Version `json:"version,omitzero"`
	Displayed   sessionpresentation.Thread  `json:"displayed,omitzero"`
	Caller      string                      `json:"caller,omitempty"`
	Cursor      string                      `json:"cursor,omitempty"`
	Publication jsontext.Value              `json:"publication,omitempty"`
	Live        *ContentReference           `json:"live,omitempty"`
}

// The shared payload excludes physical frontend identities. Keep only its
// latest detached encoding, under Server.mu, for every panel observing it.
type nativePanelKey struct {
	Attachment string
	Revisions  [4]uint64
}

type nativePanelPublication struct {
	key  nativePanelKey
	data jsontext.Value
}

func (s *Server) panelPublication(key nativePanelKey) (jsontext.Value, error) {
	if s.nativePanel != nil && s.nativePanel.key == key {
		return s.nativePanel.data, nil
	}
	review := s.review
	if review == nil {
		review = jsontext.Value("null")
	}
	publication, err := s.encodeShared("snapshot", message{Session: &s.session, Roster: s.roster, Review: review, Live: s.livePreviews}, true)
	if err != nil {
		return nil, err
	}
	s.nativePanel = &nativePanelPublication{key: key, data: publication.Data}
	return publication.Data, nil
}

// Fields through changed are guarded by Hub.mu, alongside physical connection
// membership. Selected source has its own lock: indexing cannot block delivery.
type nativeFrontend struct {
	pid        int
	instance   string
	sequence   uint64
	active     *rpcSubscription
	changed    chan struct{}
	viewers    map[string]time.Time
	revision   uint64
	selectedMu sync.Mutex
	selected   *selectedContent
}

func (c *rpcConnection) activateFrontend(params RPCParams) (any, *RPCError) {
	var request NativeActivation
	if failure := decodeBody(params.Body, &request); failure != nil {
		return nil, failure
	}
	if request.PID <= 0 || request.Sequence == 0 {
		return nil, rpcFailure(400, "A process and activation sequence are required.")
	}
	c.mu.Lock()
	sub := c.subscriptions[params.Subscription]
	valid := sub != nil && sub.active && sub.owner != nil && sub.ctx.Err() == nil
	c.mu.Unlock()
	if !valid {
		return nil, rpcFailure(409, "Native subscription is unavailable.")
	}
	sub.owner.mu.Lock()
	started := sub.view.started && sub.view.ctx.Err() == nil
	if sub.view.changed == nil {
		sub.view.changed = make(chan struct{})
	}
	sub.owner.mu.Unlock()
	if !started {
		return nil, rpcFailure(409, "Native subscription has no valid initial frame.")
	}
	h := c.endpoint.hub
	h.mu.Lock()
	f := &c.frontend
	if f.pid != 0 && f.pid != request.PID {
		h.mu.Unlock()
		return nil, rpcFailure(409, "Native process changed.")
	}
	if request.Sequence <= f.sequence {
		h.mu.Unlock()
		return nil, nil
	}
	if f.instance == "" {
		f.instance, f.viewers = rand.Text(), make(map[string]time.Time)
	}
	if f.changed != nil {
		close(f.changed)
	}
	f.changed = make(chan struct{})
	for other := range h.connections {
		if other != c && other.frontend.pid == request.PID {
			other.frontend.pid = 0
			other.frontend.active = nil
			if other.frontend.changed != nil {
				close(other.frontend.changed)
				other.frontend.changed = make(chan struct{})
			}
		}
	}
	f.pid, f.sequence, f.active = request.PID, request.Sequence, sub
	lease := f.lease(time.Now())
	h.mu.Unlock()
	if lease.Revision != 0 {
		_, failure := c.frontendIntent(context.Background(), sub, NativeOperation{Viewer: &lease})
		return nil, failure
	}
	return nil, nil
}

// lease prunes expired viewers and returns the effective absolute deadline.
// No expiry worker is needed: each physical renderer also restores at that deadline.
func (f *nativeFrontend) lease(now time.Time) NativeViewerLease {
	var expiry time.Time
	for viewer, until := range f.viewers {
		if !now.Before(until) {
			delete(f.viewers, viewer)
			continue
		}
		if until.After(expiry) {
			expiry = until
		}
	}
	out := NativeViewerLease{Revision: f.revision}
	if !expiry.IsZero() {
		out.Expires = expiry.UnixMilli()
	}
	return out
}

func (c *rpcConnection) frontendIntent(ctx context.Context, sub *rpcSubscription, operation NativeOperation) (NativeResult, *RPCError) {
	n := sub.owner
	n.mu.Lock()
	v := sub.view
	target, startup, version := v.target, v.startup, v.version
	n.mu.Unlock()
	body, _ := json.Marshal(&NativeMutation{Attachment: sub.attachment, Version: version, Operation: operation}, nativeEncoding)
	return n.request(ctx, target, startup, localNativeView, body, c.endpoint.preparations)
}

func (c *rpcConnection) frontendRequest(ctx context.Context, method string, params RPCParams) (any, *RPCError) {
	if method == "native/observe" {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 18*time.Second)
		defer cancel()
	}
	if method == "native/activate" {
		return c.activateFrontend(params)
	}
	var request NativeFrontendRequest
	if failure := decodeBody(params.Body, &request); failure != nil {
		return nil, failure
	}
	h := c.endpoint.hub
	h.mu.Lock()
	var owner *rpcConnection
	for connection := range h.connections {
		if connection.frontend.pid == request.PID && connection.ctx.Err() == nil {
			owner = connection
			break
		}
	}
	if owner == nil {
		h.mu.Unlock()
		return NativeObservation{Available: false}, nil
	}
	f := &owner.frontend
	if request.Instance != f.instance && !(method == "native/observe" && request.Instance == "" && request.After == "") {
		h.mu.Unlock()
		return nil, rpcFailure(409, "Native frontend changed; reopen observation.")
	}
	sub := f.active
	if method != "native/observe" && method != "native/release" && request.Attachment != "" && (sub == nil || request.Attachment != sub.attachment) {
		h.mu.Unlock()
		return nil, rpcFailure(409, "Native attachment changed; reopen observation.")
	}
	if sub == nil || sub.ctx.Err() != nil {
		h.mu.Unlock()
		return NativeObservation{Available: false}, nil
	}
	if method == "native/observe" || method == "native/release" {
		if request.Viewer == "" || len(request.Viewer) > 256 {
			h.mu.Unlock()
			return nil, rpcFailure(400, "A bounded viewer identity is required.")
		}
		now := time.Now()
		f.lease(now)
		if method == "native/observe" {
			if _, ok := f.viewers[request.Viewer]; !ok && len(f.viewers) >= 64 {
				h.mu.Unlock()
				return nil, rpcFailure(429, "Native viewer capacity reached.")
			}
			f.viewers[request.Viewer] = now.Add(nativeViewerLease)
		} else {
			delete(f.viewers, request.Viewer)
		}
		f.revision++
		lease := f.lease(now)
		generation := f.sequence
		h.mu.Unlock()
		for {
			// A retiring view can lose its reply after the frontend has moved.
			// Deliver the current lease snapshot to that completed destination,
			// without replaying the renewal or extending its absolute deadline.
			snapshot := lease
			_, failure := owner.frontendIntent(ctx, sub, NativeOperation{Viewer: &snapshot})
			h.mu.Lock()
			current := f.active
			if current != nil && current.ctx.Err() == nil && ctx.Err() == nil && (current != sub || f.sequence != generation) {
				sub, generation = current, f.sequence
				lease = f.lease(time.Now())
				h.mu.Unlock()
				continue
			}
			h.mu.Unlock()
			if failure != nil {
				return nil, failure
			}
			break
		}
		if method == "native/release" {
			return NativeObservation{Available: true}, nil
		}
		return owner.observeFrontend(ctx, request)
	}
	instance, activation := f.instance, f.sequence
	h.mu.Unlock()
	if method == "native/resolve" {
		sub.owner.mu.Lock()
		result := NativeObservation{Available: true, Instance: instance, Activation: activation, Thread: sub.view.target.thread, Attachment: sub.attachment, Displayed: sub.view.displayed, Caller: sub.view.caller}
		sub.owner.mu.Unlock()
		return result, nil
	}
	sub.owner.mu.Lock()
	currentAttachment, displayed := sub.attachment, sub.view.displayed.ID
	target, startup := sub.view.target, sub.view.startup
	sub.owner.mu.Unlock()
	if method == "native/select" {
		if request.Activation != activation || request.Attachment != currentAttachment || request.Displayed == "" || request.Displayed != displayed {
			return nil, rpcFailure(409, "Displayed attachment changed; reopen observation.")
		}
		// Recheck the physical active attachment at semantic admission, not just at lookup.
		h.mu.Lock()
		valid := f.instance == instance && f.active == sub && f.sequence == activation
		h.mu.Unlock()
		if !valid {
			return nil, rpcFailure(409, "Native frontend changed.")
		}
		return owner.frontendIntent(ctx, sub, NativeOperation{Navigate: &NativeAgentSelection{Activation: activation, Displayed: request.Displayed, Agent: request.Agent}})
	}
	if method == "native/content" {
		if request.Content == nil {
			return nil, rpcFailure(400, "Content reference is required.")
		}
		selected, failure := h.lookup(target.thread, startup, localNativeView)
		if failure != nil {
			return nil, failure
		}
		if request.Offset == nil {
			body, _ := json.Marshal(request.Content)
			return selected.server.content(body)
		}
		f.selectedMu.Lock()
		defer f.selectedMu.Unlock()
		return selected.server.selectedContent(*request.Content, *request.Offset, &f.selected)
	}
	return nil, rpcFailure(400, "Unknown native frontend request.")
}

func (c *rpcConnection) observeFrontend(ctx context.Context, request NativeFrontendRequest) (any, *RPCError) {
	for {
		h := c.endpoint.hub
		h.mu.Lock()
		sub, changed := c.frontend.active, c.frontend.changed
		result := NativeObservation{Available: true, Instance: c.frontend.instance, Activation: c.frontend.sequence}
		h.mu.Unlock()
		if c.ctx.Err() != nil || sub == nil || sub.ctx.Err() != nil {
			return NativeObservation{Available: false}, nil
		}
		sub.owner.mu.Lock()
		view := sub.view
		target, startup, nativeChanged := view.target, view.startup, view.changed
		result.Thread, result.Startup, result.Attachment = target.thread, startup, sub.attachment
		result.Version, result.Displayed, result.Caller = view.version, view.displayed, view.caller
		sub.owner.mu.Unlock()
		selected, failure := h.lookup(target.thread, startup, localNativeView)
		if failure != nil {
			return nil, failure
		}
		server := selected.server
		// Observe owner revisions directly. A full shared subscription would
		// materialize an unused feed snapshot for each short-lived panel read.
		server.mu.Lock()
		if server.panelChanged == nil {
			server.panelChanged = make(chan struct{})
		}
		panelChanged := server.panelChanged
		key := nativePanelKey{Attachment: server.readAttachment, Revisions: [4]uint64{server.contentVersions["session"], server.rosterRevision, server.contentVersions["review-info"], server.contentVersions["live"]}}
		identity, _ := json.Marshal(&struct {
			Instance, Attachment, Source, Caller string
			Activation                           uint64
			Displayed                            sessionpresentation.Thread
			Revisions                            [4]uint64
		}{result.Instance, result.Attachment, key.Attachment, result.Caller, result.Activation, result.Displayed, key.Revisions}, encoding)
		result.Cursor = fmt.Sprintf("%x", sha256.Sum256(identity))
		ready := server.published
		if ready && (result.Cursor != request.After || ctx.Err() == context.DeadlineExceeded) {
			if result.Cursor == request.After {
				server.mu.Unlock()
				return result, nil
			}
			publication, err := server.panelPublication(key)
			result.Publication = publication
			result.Live = &ContentReference{Kind: "live", Attachment: server.readAttachment, Revision: server.contentVersions["live"]}
			server.mu.Unlock()
			if err != nil {
				return nil, rpcFailure(500, "Shared publication unavailable.")
			}
			return result, nil
		}
		server.mu.Unlock()
		select {
		case <-c.ctx.Done():
			return NativeObservation{Available: false}, nil
		case <-ctx.Done():
			if !ready || ctx.Err() != context.DeadlineExceeded {
				return nil, rpcFailure(503, "Native observation canceled.")
			}
		case <-sub.ctx.Done():
		case <-changed:
		case <-nativeChanged:
		case <-panelChanged:
		}
	}
}
