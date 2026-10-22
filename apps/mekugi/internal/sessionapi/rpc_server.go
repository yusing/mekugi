package sessionapi

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

const (
	requestLimit      = 8
	preparationLimit  = 4
	subscriptionLimit = 64
)

// rpcMethods is the control admission catalog. Body limits and native authority
// are checked before dispatch, including methods whose body is otherwise ignored.
type rpcMethod struct {
	limit  int
	native bool
}

var rpcMethods = map[string]rpcMethod{
	"session/options":     {limit: 1024},
	"session/list":        {limit: 1024},
	"session/read":        {limit: 1024},
	"session/start":       {limit: InputBodyLimit},
	"session/end":         {limit: 1024},
	"session/subscribe":   {limit: 1024},
	"session/input":       {limit: InputBodyLimit},
	"session/shell":       {limit: inputLimit},
	"session/answer":      {limit: 64 << 10},
	"session/decide":      {limit: 64 << 10},
	"session/interrupt":   {limit: 1024},
	"session/redraw":      {limit: 1024, native: true},
	"session/control":     {limit: 1024},
	"view/attach":         {limit: 1024},
	"view/intent":         {limit: InputBodyLimit},
	"native/start":        {limit: inputLimit, native: true},
	"subscription/cancel": {limit: 1024},
	"request/cancel":      {limit: 1024},
}

func rpcFailure(status int, message string) *RPCError {
	return &RPCError{Code: -32000, Message: message, Data: &RPCErrorData{Status: status, Outcome: "not_admitted"}}
}

func rpcUnknown(message string) *RPCError {
	return &RPCError{Code: -32000, Message: message, Data: &RPCErrorData{Status: 503, Outcome: "unknown"}}
}

func decodeBody(body jsontext.Value, value any) *RPCError {
	if body.Kind() != '{' {
		return rpcFailure(400, "request body must be an object")
	}
	if err := json.Unmarshal(body, value); err != nil {
		return rpcFailure(400, "invalid request: "+err.Error())
	}
	return nil
}

type rpcSubscription struct {
	id      string
	ctx     context.Context
	cancel  context.CancelFunc
	events  <-chan publication
	native  *nativePublications
	values  <-chan []byte
	kind    string
	cleanup func()
	once    sync.Once
	active  bool // guarded by connection.mu
	exited  bool // writer-owned
}

func newRPCSubscription(ctx context.Context, events <-chan publication, cleanup func()) *rpcSubscription {
	ctx, cancel := context.WithCancel(ctx)
	return &rpcSubscription{ctx: ctx, cancel: cancel, events: events, cleanup: cleanup}
}

func (s *rpcSubscription) close() {
	s.once.Do(func() {
		s.cancel()
		if s.cleanup != nil {
			s.cleanup()
		}
	})
}

type rpcEndpoint struct {
	hub          *Hub
	preparations chan struct{}
}

func (e *rpcEndpoint) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+RPCPath, func(w http.ResponseWriter, r *http.Request) { e.serve(w, r, remoteHumanView) })
	mux.HandleFunc("GET "+NativeRPCPath, func(w http.ResponseWriter, r *http.Request) { e.serve(w, r, localNativeView) })
	return mux
}

type rpcReply struct {
	message RPCMessage
	sent    func()
}

type rpcConnection struct {
	endpoint      *rpcEndpoint
	wire          *websocket.Conn
	authority     viewAuthority
	ctx           context.Context
	cancel        context.CancelFunc
	mu            sync.Mutex
	requests      map[string]context.CancelFunc
	subscriptions map[string]*rpcSubscription
	responses     chan rpcReply
	wake          chan struct{}
	slots         chan struct{}
	wg            sync.WaitGroup
	draining      bool
	drainTimer    *time.Timer
}

func (e *rpcEndpoint) serve(w http.ResponseWriter, r *http.Request, authority viewAuthority) {
	if !strings.Contains(","+strings.ReplaceAll(r.Header.Get("Sec-WebSocket-Protocol"), " ", "")+",", ","+RPCSubprotocol+",") {
		http.Error(w, "incompatible Mekugi control subprotocol", http.StatusConflict)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	c := &rpcConnection{endpoint: e, authority: authority, ctx: ctx, cancel: cancel, requests: make(map[string]context.CancelFunc), subscriptions: make(map[string]*rpcSubscription), responses: make(chan rpcReply, subscriberBuffer), wake: make(chan struct{}, 1), slots: make(chan struct{}, requestLimit)}
	h := e.hub
	h.mu.Lock()
	if h.stopping || h.ctx.Err() != nil {
		h.mu.Unlock()
		http.Error(w, "Server stopping.", 503)
		return
	}
	h.connections[c] = struct{}{}
	h.connectionWG.Add(1)
	h.mu.Unlock()
	defer func() { h.mu.Lock(); delete(h.connections, c); h.mu.Unlock(); h.connectionWG.Done() }()
	wire, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{RPCSubprotocol}})
	if err != nil {
		return
	}
	c.wire = wire
	wire.SetReadLimit(RPCMessageLimit)
	defer wire.CloseNow()
	c.run()
}

// requestID normalizes JSON IDs so alternative string escapes cannot evade
// duplicate pending-request admission. Integer IDs remain exact, not floats.
func requestID(id jsontext.Value) (string, bool) {
	if len(id) > 256 {
		return "", false
	}
	switch id.Kind() {
	case '"':
		var value string
		if json.Unmarshal(id, &value) == nil {
			return "s:" + value, true
		}
	case '0':
		var value int64
		if json.Unmarshal(id, &value) == nil {
			return "n:" + strconv.FormatInt(value, 10), true
		}
	}
	return "", false
}

func (c *rpcConnection) read(ctx context.Context) (RPCMessage, *RPCError, error) {
	kind, data, err := c.wire.Read(ctx)
	if err != nil {
		return RPCMessage{}, nil, err
	}
	var request RPCMessage
	if kind != websocket.MessageText || jsontext.Value(data).Kind() != '{' || json.Unmarshal(data, &request, json.RejectUnknownMembers(true)) != nil {
		return request, &RPCError{Code: -32600, Message: "Expected one JSON-RPC request object."}, nil
	}
	_, validID := requestID(request.ID)
	if request.JSONRPC != "2.0" || !validID || request.Method == "" || request.Result != nil || request.Error != nil || request.Params.Kind() != '{' {
		return request, &RPCError{Code: -32600, Message: "Invalid JSON-RPC request."}, nil
	}
	return request, nil, nil
}

func (c *rpcConnection) run() {
	defer func() {
		c.cancel()
		_ = c.wire.CloseNow()
		c.wg.Wait()
		c.mu.Lock()
		if c.drainTimer != nil {
			c.drainTimer.Stop()
		}
		for _, subscription := range c.subscriptions {
			subscription.close()
		}
		clear(c.subscriptions)
		c.mu.Unlock()
	}()
	initializeContext, cancel := context.WithTimeout(c.ctx, 10*time.Second)
	request, failure, err := c.read(initializeContext)
	cancel()
	if err != nil {
		return
	}
	if failure == nil {
		var initialize struct {
			Version string `json:"version"`
		}
		if request.Method != "initialize" || json.Unmarshal(request.Params, &initialize, json.RejectUnknownMembers(true)) != nil || initialize.Version != ProtocolVersion {
			failure = rpcFailure(409, "Initialize with the supported control version before using this connection.")
		}
	}
	var result jsontext.Value
	if failure == nil {
		result, _ = json.Marshal(RPCInitialize{Version: ProtocolVersion})
	}
	if !c.write(RPCMessage{JSONRPC: "2.0", ID: responseID(request.ID), Result: result, Error: failure}) || failure != nil {
		return
	}
	c.wg.Go(c.writer)
	for c.ctx.Err() == nil {
		request, failure, err := c.read(c.ctx)
		if err != nil {
			return
		}
		if failure != nil {
			c.reply(request.ID, nil, failure, nil)
			continue
		}
		c.mu.Lock()
		draining := c.draining
		c.mu.Unlock()
		if draining {
			c.reply(request.ID, nil, rpcFailure(503, "Server stopping."), nil)
			continue
		}
		if request.Method == "initialize" {
			c.reply(request.ID, nil, rpcFailure(409, "Connection is already initialized."), nil)
			continue
		}
		method, known := rpcMethods[request.Method]
		if !known {
			c.reply(request.ID, nil, &RPCError{Code: -32601, Message: "Unknown method."}, nil)
			continue
		}
		var params RPCParams
		if json.Unmarshal(request.Params, &params, json.RejectUnknownMembers(true)) != nil || params.Body != nil && params.Body.Kind() != '{' {
			c.reply(request.ID, nil, rpcFailure(400, "Invalid method parameters."), nil)
			continue
		}
		if len(params.Body) > method.limit {
			c.reply(request.ID, nil, rpcFailure(413, "request body too large"), nil)
			continue
		}
		if method.native && c.authority != localNativeView {
			c.reply(request.ID, nil, rpcFailure(403, "This method requires the native endpoint."), nil)
			continue
		}
		key, _ := requestID(request.ID)
		c.mu.Lock()
		_, duplicate := c.requests[key]
		c.mu.Unlock()
		if duplicate {
			c.reply(request.ID, nil, rpcFailure(409, "Request ID is already pending."), nil)
			continue
		}
		// Cancellation never waits behind ordinary method execution or bulk work.
		if request.Method == "request/cancel" {
			var original struct {
				ID jsontext.Value `json:"id"`
			}
			failure := decodeBody(params.Body, &original)
			originalKey, valid := requestID(original.ID)
			if failure == nil && !valid {
				failure = rpcFailure(400, "A request ID is required.")
			}
			if failure == nil {
				c.mu.Lock()
				if cancel := c.requests[originalKey]; cancel != nil {
					cancel()
				}
				c.mu.Unlock()
			}
			c.reply(request.ID, nil, failure, nil)
			continue
		}
		if request.Method == "subscription/cancel" {
			c.removeSubscription(params.Subscription, nil, nil)
			c.reply(request.ID, nil, nil, nil)
			continue
		}
		select {
		case c.slots <- struct{}{}:
		default:
			c.reply(request.ID, nil, rpcFailure(503, "Too many pending control requests."), nil)
			continue
		}
		ctx, cancel := context.WithCancel(c.ctx)
		c.mu.Lock()
		c.requests[key] = cancel
		c.mu.Unlock()
		c.wg.Go(func() {
			defer func() {
				cancel()
				c.mu.Lock()
				delete(c.requests, key)
				c.mu.Unlock()
				<-c.slots
				c.signal()
			}()
			value, failure, sent := c.dispatch(ctx, request.Method, params)
			c.reply(request.ID, value, failure, sent)
		})
	}
}

func responseID(id jsontext.Value) jsontext.Value {
	if _, ok := requestID(id); !ok {
		return jsontext.Value("null")
	}
	return id
}

func (c *rpcConnection) reply(id jsontext.Value, value any, failure *RPCError, sent func()) {
	var result jsontext.Value
	if failure == nil {
		var err error
		result, err = json.Marshal(value, nativeEncoding)
		if err != nil {
			failure = rpcUnknown("Cannot encode control result.")
		}
	}
	reply := rpcReply{message: RPCMessage{JSONRPC: "2.0", ID: responseID(id), Result: result, Error: failure}, sent: sent}
	select {
	case c.responses <- reply:
	case <-c.ctx.Done():
	default:
		c.cancel() // A blocked physical connection cannot retain unbounded replies.
	}
}

func (c *rpcConnection) write(message RPCMessage) bool {
	data, err := json.Marshal(message, nativeEncoding)
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(c.ctx, 5*time.Second)
	defer cancel()
	return c.wire.Write(ctx, websocket.MessageText, data) == nil
}

// beginDrain lets the single writer flush already-admitted replies and terminal
// publications. A stalled physical connection still ends with transport loss.
func (c *rpcConnection) beginDrain() {
	c.mu.Lock()
	if c.draining || c.ctx.Err() != nil {
		c.mu.Unlock()
		return
	}
	c.draining = true
	c.drainTimer = time.AfterFunc(answerWithin, c.cancel)
	c.mu.Unlock()
	c.signal()
}

func (c *rpcConnection) signal() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// writer selects directly across independently bounded subscription queues.
// A noisy subscription cannot fill another subscription's queue or monopolize
// the replies channel. reflect.Select provides the same fair choice as select.
func (c *rpcConnection) writer() {
	defer c.cancel()
	for c.ctx.Err() == nil {
		cases := []reflect.SelectCase{
			{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(c.ctx.Done())},
			{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(c.responses)},
			{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(c.wake)},
		}
		var selected []*rpcSubscription
		c.mu.Lock()
		for _, subscription := range c.subscriptions {
			if !subscription.active {
				continue
			}
			var events any = subscription.events
			if subscription.values != nil {
				events = subscription.values
			} else if subscription.native != nil {
				events = subscription.native.ready
			}
			cases = append(cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(events)}, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(subscription.ctx.Done())})
			selected = append(selected, subscription)
		}
		if c.draining && len(c.requests) == 0 {
			cases = append(cases, reflect.SelectCase{Dir: reflect.SelectDefault})
		}
		c.mu.Unlock()
		index, value, open := reflect.Select(cases)
		if cases[index].Dir == reflect.SelectDefault {
			return
		}
		switch index {
		case 0:
			return
		case 1:
			reply := value.Interface().(rpcReply)
			if !c.write(reply.message) {
				return
			}
			if reply.sent != nil {
				reply.sent()
			}
			continue
		case 2:
			continue
		}
		subscription := selected[(index-3)/2]
		if (index-3)%2 == 1 || !open {
			var failure *RPCError
			if !subscription.exited {
				failure = rpcFailure(503, "Subscription ended; reconnect for a fresh snapshot.")
				if cause := context.Cause(subscription.ctx); cause != nil && !errors.Is(cause, context.Canceled) {
					failure = rpcFailure(503, cause.Error())
				}
			}
			c.removeSubscription(subscription.id, subscription, failure)
			continue
		}
		var event publication
		if subscription.values != nil {
			event = publication{Kind: subscription.kind, Data: value.Interface().([]byte)}
		} else if subscription.native != nil {
			var ok bool
			event, ok = subscription.native.pop()
			if !ok {
				continue
			}
		} else {
			event = value.Interface().(publication)
		}
		c.mu.Lock()
		current := c.subscriptions[subscription.id] == subscription
		c.mu.Unlock()
		if !current {
			continue
		}
		params, _ := json.Marshal(RPCEvent{Subscription: subscription.id, Kind: event.Kind, Data: event.Data}, encoding)
		if !c.write(RPCMessage{JSONRPC: "2.0", Method: "subscription/event", Params: params}) {
			return
		}
		if event.Kind == "exit" {
			subscription.exited = true
			c.removeSubscription(subscription.id, subscription, nil)
		}
	}
}

func (c *rpcConnection) removeSubscription(id string, expected *rpcSubscription, failure *RPCError) {
	c.mu.Lock()
	subscription := c.subscriptions[id]
	if subscription == nil || expected != nil && subscription != expected {
		c.mu.Unlock()
		return
	}
	// Publish closure before allowing this ID to be reused. A writer holding an
	// older select snapshot must never retire a replacement subscription.
	params, _ := json.Marshal(RPCClosed{Subscription: id, Error: failure})
	select {
	case c.responses <- rpcReply{message: RPCMessage{JSONRPC: "2.0", Method: "subscription/closed", Params: params}}:
	case <-c.ctx.Done():
	default:
		c.cancel()
	}
	delete(c.subscriptions, id)
	c.mu.Unlock()
	subscription.close()
	c.signal()
}

func (c *rpcConnection) subscribe(ctx context.Context, id string, build func(context.Context) (*rpcSubscription, *RPCError)) (any, *RPCError, func()) {
	if id == "" || len(id) > 256 {
		return nil, rpcFailure(400, "A bounded nonempty subscription ID is required."), nil
	}
	viewContext, viewCancel := context.WithCancel(c.ctx)
	reserved := newRPCSubscription(viewContext, nil, viewCancel)
	c.mu.Lock()
	if _, exists := c.subscriptions[id]; exists || len(c.subscriptions) >= subscriptionLimit {
		c.mu.Unlock()
		reserved.close()
		return nil, rpcFailure(409, "Subscription ID is active or the connection subscription limit was reached."), nil
	}
	c.subscriptions[id] = reserved
	c.mu.Unlock()
	stop := context.AfterFunc(ctx, viewCancel)
	subscription, failure := build(viewContext)
	stop()
	c.mu.Lock()
	if failure != nil || c.subscriptions[id] != reserved || viewContext.Err() != nil {
		if c.subscriptions[id] == reserved {
			delete(c.subscriptions, id)
		}
		c.mu.Unlock()
		reserved.close()
		if subscription != nil {
			subscription.close()
		}
		if failure == nil {
			failure = rpcUnknown("Subscription admission was canceled.")
		}
		return nil, failure, nil
	}
	subscription.id = id
	cleanup := subscription.cleanup
	subscription.cleanup = func() {
		if cleanup != nil {
			cleanup()
		}
		reserved.close()
	}
	c.subscriptions[id] = subscription
	c.mu.Unlock()
	return RPCSubscribed{Subscription: id}, nil, func() {
		c.mu.Lock()
		if c.subscriptions[id] == subscription {
			subscription.active = true
		}
		c.mu.Unlock()
		c.signal()
	}
}

func (c *rpcConnection) dispatch(ctx context.Context, method string, params RPCParams) (any, *RPCError, func()) {
	h := c.endpoint.hub
	switch method {
	case "session/options":
		value, err := h.startOptions(ctx)
		return value, err, nil
	case "session/start":
		value, err := h.startSession(ctx, params.Body)
		return value, err, nil
	case "native/start":
		return c.subscribe(ctx, params.Subscription, func(viewContext context.Context) (*rpcSubscription, *RPCError) {
			return h.startNative(ctx, viewContext, params.Body)
		})
	case "session/list":
		h.mu.Lock()
		value := jsontext.Value(h.list)
		h.mu.Unlock()
		return value, nil, nil
	case "session/subscribe":
		if params.Thread == "" && params.Startup == "" {
			return c.subscribe(ctx, params.Subscription, func(viewContext context.Context) (*rpcSubscription, *RPCError) {
				values := make(chan []byte, 1)
				h.mu.Lock()
				values <- h.list
				h.viewers[values] = struct{}{}
				h.mu.Unlock()
				subscription := newRPCSubscription(viewContext, nil, func() { h.mu.Lock(); delete(h.viewers, values); h.mu.Unlock() })
				subscription.values, subscription.kind = values, "sessions"
				return subscription, nil
			})
		}
	}
	selected, failure := h.lookup(params.Thread, params.Startup, c.authority)
	if failure != nil {
		return nil, failure, nil
	}
	target := requestTarget{selected.entry.Thread, selected.entry.Workspace}
	if !selected.server.live && (method == "view/attach" || method == "view/intent") {
		return nil, rpcFailure(405, "Recorded playback has no live control view."), nil
	}
	switch method {
	case "session/read":
		return selected.entry, nil, nil
	case "session/end":
		if selected.runtime == nil {
			return nil, rpcFailure(405, "This publication has no owned runtime."), nil
		}
		selected.runtime.cancel()
		return nil, nil, nil
	case "view/attach":
		return c.subscribe(ctx, params.Subscription, func(viewContext context.Context) (*rpcSubscription, *RPCError) {
			return selected.server.Native().attach(ctx, viewContext, target, params.Startup, c.authority)
		})
	case "session/subscribe":
		return c.subscribe(ctx, params.Subscription, func(viewContext context.Context) (*rpcSubscription, *RPCError) {
			viewContext, cancel := context.WithCancel(viewContext)
			stop := func() bool { return true }
			if selected.runtime != nil {
				stop = context.AfterFunc(selected.runtime.ctx, cancel)
			}
			subscription, failure := selected.server.subscribe(viewContext)
			if failure != nil {
				stop()
				cancel()
				return nil, failure
			}
			cleanup := subscription.cleanup
			subscription.cleanup = func() { stop(); cancel(); cleanup() }
			return subscription, nil
		})
	case "view/intent":
		value, err := selected.server.Native().request(ctx, target, params.Startup, c.authority, params.Body, c.endpoint.preparations)
		return value, err, nil
	default:
		if selected.runtime != nil {
			var cancel context.CancelFunc
			ctx, cancel = context.WithCancel(ctx)
			stop := context.AfterFunc(selected.runtime.ctx, cancel)
			defer stop()
			defer cancel()
		}
		value, err := selected.server.request(ctx, target, method, params.Body, c.endpoint.preparations)
		return value, err, nil
	}
}
