package router

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"strings"
	"sync"
	"time"
)

const liveDiffEventsPath = "/internal/live-diff"
const maxLiveDiffEventBytes = 1 << 20

// The bootstrap file is a private connection capability, not changing view data.
type liveDiffConnection struct {
	Endpoint string
	Token    string
}

type liveDiffChange struct {
	Workspace string
	Thread    string
	Stream    int
	ID        string
	Change    trackedChange
}

type liveDiffEvent struct {
	Kind    string
	Scope   *liveDiffScope   `json:",omitempty"`
	Changes []liveDiffChange `json:",omitempty"`
	Status  string           `json:",omitempty"`
	Resync  bool             `json:",omitzero"`
}

type liveDiffSubscriber struct {
	events chan liveDiffEvent
	gap    chan struct{}
}

type liveDiffProducerRoute struct {
	workspace, thread, change, handle string
	expires                           time.Time
	pending, connected, unavailable   bool
}

// The router owns this hub. Enqueueing never waits for a renderer or performs
// network I/O, including when called at the durable publication boundary.
type liveDiffBroker struct {
	ctx          context.Context
	mu           sync.Mutex
	connection   liveDiffConnection
	scope        liveDiffScope
	subs         map[*liveDiffSubscriber]bool
	producers    map[string]*liveDiffProducerRoute
	coverageLost bool
}

func newLiveDiffBroker(ctx context.Context) *liveDiffBroker {
	return &liveDiffBroker{
		ctx: ctx, connection: liveDiffConnection{Token: rand.Text()},
		scope: liveDiffScope{Workspaces: make(map[string]map[string]bool)},
		subs:  make(map[*liveDiffSubscriber]bool), producers: make(map[string]*liveDiffProducerRoute),
	}
}

func (b *liveDiffBroker) setEndpoint(endpoint string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.connection.Endpoint = endpoint
}

func (b *liveDiffBroker) descriptor() liveDiffConnection {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.connection
}

func cloneLiveDiffScope(scope liveDiffScope) liveDiffScope {
	next := liveDiffScope{Workspaces: make(map[string]map[string]bool, len(scope.Workspaces))}
	for workspace, threads := range scope.Workspaces {
		next.Workspaces[workspace] = maps.Clone(threads)
	}
	return next
}

func (b *liveDiffBroker) statusLocked() string {
	if b.coverageLost {
		return "UNAVAILABLE: edit publisher coverage lost for this session"
	}
	pending := false
	for _, route := range b.producers {
		if route.unavailable {
			return "UNAVAILABLE: edit publisher disconnected"
		}
		pending = pending || route.pending
	}
	if pending {
		return "WAITING: edit publisher connecting"
	}
	return ""
}

func (b *liveDiffBroker) scopeEventLocked() liveDiffEvent {
	scope := b.scope // setScope replaces, never mutates, the broker-owned maps.
	return liveDiffEvent{Kind: "scope", Scope: &scope, Status: b.statusLocked()}
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

func (b *liveDiffBroker) subscribe() *liveDiffSubscriber {
	b.mu.Lock()
	defer b.mu.Unlock()
	sub := &liveDiffSubscriber{events: make(chan liveDiffEvent, 32), gap: make(chan struct{})}
	// Register before exposing the initial scope. The client may take its
	// durable snapshot while subsequent events accumulate in this queue.
	b.subs[sub] = true
	event := b.scopeEventLocked()
	event.Resync = true
	sub.events <- event
	return sub
}

func (b *liveDiffBroker) serveEvents(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+b.descriptor().Token {
		http.Error(w, "invalid live diff capability", http.StatusUnauthorized)
		return
	}
	sub := b.subscribe()
	defer func() {
		b.mu.Lock()
		delete(b.subs, sub)
		b.mu.Unlock()
	}()
	controller := http.NewResponseController(w)
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	write := func(event liveDiffEvent) error {
		data, err := json.Marshal(event)
		if err != nil || len(data) > maxLiveDiffEventBytes {
			return errors.New("live diff event exceeds capacity")
		}
		if err := controller.SetWriteDeadline(time.Now().Add(2 * time.Second)); err != nil {
			return err
		}
		if _, err := w.Write(append(data, '\n')); err != nil {
			return err
		}
		return controller.Flush()
	}
	heartbeat := time.NewTicker(10 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-b.ctx.Done():
			_ = write(liveDiffEvent{Kind: "end"})
			return
		case <-r.Context().Done():
			return
		case <-sub.gap:
			_ = write(liveDiffEvent{Kind: "reset"})
			return
		case event := <-sub.events:
			if write(event) != nil {
				return
			}
		case <-heartbeat.C:
			if write(liveDiffEvent{Kind: "heartbeat"}) != nil {
				return
			}
		}
	}
}

// Registration precedes carrier exposure. An absent worker is visible as a
// coverage gap rather than letting the pane silently claim to be up to date.
func (b *liveDiffBroker) expectProducer(workspace, thread, change, handle string) liveDiffConnection {
	b.mu.Lock()
	defer b.mu.Unlock()
	for token, route := range b.producers {
		if time.Now().After(route.expires) && !route.connected {
			b.coverageLost = b.coverageLost || route.pending || route.unavailable
			delete(b.producers, token)
		}
	}
	if len(b.producers) >= 256 || b.connection.Endpoint == "" {
		b.coverageLost = true
		b.emitLocked(liveDiffEvent{Kind: "coverage", Status: b.statusLocked()})
		return liveDiffConnection{}
	}
	connection := liveDiffConnection{Endpoint: b.connection.Endpoint + "/producer", Token: rand.Text()}
	b.producers[connection.Token] = &liveDiffProducerRoute{
		workspace: workspace, thread: thread, change: change, handle: handle,
		expires: time.Now().Add(shellArtifactTTL), pending: true,
	}
	b.emitLocked(liveDiffEvent{Kind: "coverage", Status: b.statusLocked()})
	return connection
}

// A resume reuses the descriptor already retained for the worker. Never allocate
// a replacement that the consuming control channel would not know about.
func (b *liveDiffBroker) resumeProducer(workspace, thread, change, handle string, connection liveDiffConnection) {
	b.mu.Lock()
	defer b.mu.Unlock()
	route := b.producers[connection.Token]
	if route == nil && connection.Token != "" &&
		connection.Endpoint == b.connection.Endpoint+"/producer" && len(b.producers) < 256 {
		// Completed workers release capacity. A later native continuation
		// registers the same retained capability, never a replacement token.
		route = &liveDiffProducerRoute{
			workspace: workspace, thread: thread, change: change, handle: handle,
			expires: time.Now().Add(shellArtifactTTL),
		}
		b.producers[connection.Token] = route
	}
	if route == nil || route.handle != handle || time.Now().After(route.expires) {
		b.coverageLost = true
	} else {
		route.pending = true // Reserve the replacement before the old worker closes.
	}
	b.emitLocked(liveDiffEvent{Kind: "coverage", Status: b.statusLocked()})
}

type liveDiffProducerMessage struct {
	Changes []liveDiffChange `json:",omitempty"`
	Done    bool             `json:",omitempty"`
}

func (b *liveDiffBroker) serveProducer(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	b.mu.Lock()
	route := b.producers[token]
	if route == nil || route.connected || time.Now().After(route.expires) {
		b.mu.Unlock()
		http.Error(w, "invalid live diff publisher", http.StatusUnauthorized)
		return
	}
	reconcile := route.unavailable
	route.pending, route.connected, route.unavailable = false, true, false
	if reconcile {
		event := b.scopeEventLocked()
		event.Resync = true
		b.emitLocked(event)
	} else {
		b.emitLocked(liveDiffEvent{Kind: "coverage", Status: b.statusLocked()})
	}
	b.mu.Unlock()
	complete := false
	defer func() {
		b.mu.Lock()
		route.connected, route.unavailable = false, !complete
		if complete {
			if !route.pending {
				delete(b.producers, token)
			}
			// Done follows every queued publication. A clean close changes
			// coverage only; it must not reread historical capture files.
			b.emitLocked(liveDiffEvent{Kind: "coverage", Status: b.statusLocked()})
		} else {
			// A crash or failed drain can leave a durable, unannounced edit.
			event := b.scopeEventLocked()
			event.Resync = true
			b.emitLocked(event)
		}
		b.mu.Unlock()
	}()
	// Stop only this auxiliary request when the router exits. An idle worker
	// must not keep HTTP shutdown waiting for the retained handle's lifetime.
	_ = http.NewResponseController(w).SetReadDeadline(time.Time{})
	stop := context.AfterFunc(b.ctx, func() {
		_ = http.NewResponseController(w).SetReadDeadline(time.Now())
	})
	defer stop()
	scanner := bufio.NewScanner(r.Body)
	scanner.Buffer(make([]byte, 4096), maxLiveDiffEventBytes)
	for scanner.Scan() {
		var message liveDiffProducerMessage
		if json.Unmarshal(scanner.Bytes(), &message) != nil {
			http.Error(w, "invalid live diff publication", http.StatusBadRequest)
			return
		}
		if message.Done {
			complete = true
			return
		}
		for _, change := range message.Changes {
			if change.Workspace != route.workspace || change.Thread != route.thread || change.ID != route.change {
				http.Error(w, "live diff publication identity mismatch", http.StatusForbidden)
				return
			}
			for _, call := range change.Change.Calls {
				if !strings.HasPrefix(call.ID, route.handle+"-") {
					http.Error(w, "live diff attempt belongs to another operation", http.StatusForbidden)
					return
				}
			}
		}
		b.publish(message.Changes) // An empty batch is the connection handshake.
	}
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
				changes = append(changes, liveDiffChange{
					Workspace: index.Workspace, Thread: info.Thread, Stream: stream, ID: id,
					Change: trackedChange{Correlation: index.Changes[id].Correlation, Calls: calls},
				})
				break
			}
		}
	}
	s.liveDiff(changes)
}

func liveDiffRequest(ctx context.Context, connection liveDiffConnection, method string, body io.Reader) (*http.Request, error) {
	if !strings.HasPrefix(connection.Endpoint, "http://127.0.0.1:") {
		return nil, fmt.Errorf("invalid local live diff endpoint")
	}
	req, err := http.NewRequestWithContext(ctx, method, connection.Endpoint, body)
	if err == nil {
		req.Header.Set("Authorization", "Bearer "+connection.Token)
	}
	return req, err
}
