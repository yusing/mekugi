package sessionapi

import (
	"context"
	"crypto/rand"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// RPCClient owns a continuously read, multiplexed control connection. Calls are
// never replayed. Closing it releases subscriptions, never admitted execution.
type RPCClient struct {
	conn          *websocket.Conn
	ctx           context.Context
	cancel        context.CancelFunc
	done          chan struct{}
	write         chan struct{}
	mu            sync.Mutex
	next          uint64
	pending       map[string]chan RPCMessage
	subscriptions map[string]*RPCSubscription
	err           error
}

type RPCSubscription struct {
	client *RPCClient
	ID     string
	events []RPCEvent
	ready  chan struct{}
	native bool
	bytes  int
	err    error
}

// OpenRPC initializes exactly one endpoint. Token is used only for the remote
// connector. The supplied HTTP client retains Unix peer admission when local.
func OpenRPC(ctx context.Context, client *http.Client, endpoint, token string) (*RPCClient, error) {
	if client == nil {
		client = http.DefaultClient
	}
	local := *client
	local.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	conn, response, err := websocket.Dial(ctx, endpoint, &websocket.DialOptions{
		HTTPClient: &local, HTTPHeader: http.Header{ProtocolHeader: []string{ProtocolVersion}},
		Subprotocols: []string{RPCSubprotocol}, CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		if response != nil && response.StatusCode != http.StatusSwitchingProtocols {
			if response.Body != nil {
				response.Body.Close()
			}
			return nil, &RPCError{Code: -32000, Message: "Control connection was refused.", Data: &RPCErrorData{Status: response.StatusCode, Outcome: "not_admitted"}}
		}
		return nil, err
	}
	if conn.Subprotocol() != RPCSubprotocol {
		conn.CloseNow()
		return nil, errors.New("incompatible control WebSocket protocol")
	}
	conn.SetReadLimit(64 << 20)
	lifetime, cancel := context.WithCancel(ctx)
	c := &RPCClient{conn: conn, ctx: lifetime, cancel: cancel, done: make(chan struct{}), write: make(chan struct{}, 1), pending: make(map[string]chan RPCMessage), subscriptions: make(map[string]*RPCSubscription)}
	go c.read()
	initContext, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	var result RPCInitialize
	if err := c.Call(initContext, "initialize", RPCInitialize{Version: ProtocolVersion, Token: token}, &result); err != nil {
		c.Close()
		return nil, err
	}
	if result.Version != ProtocolVersion {
		c.Close()
		return nil, errors.New("incompatible control initialization")
	}
	return c, nil
}

func (c *RPCClient) Close() error {
	c.fail(errors.New("control connection closed"))
	<-c.done
	return nil
}

func (c *RPCClient) fail(err error) {
	c.mu.Lock()
	if c.err == nil {
		c.err = err
		for id, s := range c.subscriptions {
			s.err = err
			close(s.ready)
			delete(c.subscriptions, id)
		}
	}
	c.mu.Unlock()
	c.cancel()
	c.conn.CloseNow()
}

func (c *RPCClient) read() {
	defer close(c.done)
	defer c.fail(errors.New("control connection ended"))
	for {
		kind, data, err := c.conn.Read(c.ctx)
		if err != nil {
			c.fail(err)
			return
		}
		var message RPCMessage
		if kind != websocket.MessageText || json.Unmarshal(data, &message) != nil || message.JSONRPC != "2.0" {
			c.fail(errors.New("invalid control message"))
			return
		}
		if len(message.ID) != 0 {
			c.mu.Lock()
			reply := c.pending[string(message.ID)]
			c.mu.Unlock()
			if reply != nil {
				select {
				case reply <- message:
				default:
					c.fail(errors.New("duplicate control response"))
					return
				}
			}
			continue
		}
		switch message.Method {
		case "subscription/event":
			var event RPCEvent
			if json.Unmarshal(message.Params, &event) != nil || event.Subscription == "" {
				c.fail(errors.New("invalid subscription publication"))
				return
			}
			c.mu.Lock()
			s := c.subscriptions[event.Subscription]
			if s != nil {
				var replace bool
				if !s.native && event.Kind == "snapshot" {
					// Shared snapshots reset all feed identity, including sequence
					// reuse after a seek or attachment replacement.
					clear(s.events)
					s.events, s.bytes = s.events[:0], 0
				}
				if len(s.events) > 0 {
					last := s.events[len(s.events)-1]
					previous, next := publication{Kind: last.Kind, Data: last.Data}, publication{Kind: event.Kind, Data: event.Data}
					var merged publication
					if s.native {
						merged, replace = coalesceNative(previous, next)
					} else if event.Kind == "sessions" && last.Kind == "sessions" {
						merged, replace = next, true
					} else {
						merged, replace = coalesceShared(previous, next)
					}
					if replace {
						event.Kind, event.Data = merged.Kind, merged.Data
					}
				}
				if s.native {
					normalized := normalizeNativeOutput(publication{Kind: event.Kind, Data: event.Data})
					event.Data = normalized.Data
				}
				queuedBytes, queuedCount := s.bytes+len(event.Data), len(s.events)+1
				if replace {
					queuedBytes -= len(s.events[len(s.events)-1].Data)
					queuedCount--
				}
				// Runtime exit owns a reserved terminal slot even when every
				// ordinary slot contains an irreducible ordered delivery.
				if len(event.Data) > subscriptionBytes || event.Kind != "exit" && (queuedBytes > subscriptionBytes || queuedCount > subscriberBuffer) {
					s.err = errors.New("control subscription exceeded its retained delivery capacity")
					close(s.ready)
					delete(c.subscriptions, s.ID)
					c.mu.Unlock()
					go c.cancelSubscription(s.ID)
					continue
				}
				if replace {
					s.events[len(s.events)-1] = event
				} else {
					s.events = append(s.events, event)
				}
				s.bytes = queuedBytes
				select {
				case s.ready <- struct{}{}:
				default:
				}
			}
			c.mu.Unlock()
		case "subscription/closed":
			var end RPCClosed
			if json.Unmarshal(message.Params, &end) != nil {
				c.fail(errors.New("invalid subscription termination"))
				return
			}
			c.mu.Lock()
			if s := c.subscriptions[end.Subscription]; s != nil {
				s.err = errors.New("control subscription ended")
				if end.Error != nil {
					s.err = end.Error
				}
				close(s.ready)
				delete(c.subscriptions, s.ID)
			}
			c.mu.Unlock()
		default:
			c.fail(errors.New("unknown control notification"))
			return
		}
	}
}

// Call correlates one RPC. A response failure after writing leaves mutation
// outcome unknown. Cancellation requests release preparation, not host work.
func (c *RPCClient) Call(ctx context.Context, method string, params, result any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	body, err := json.Marshal(params, nativeEncoding)
	if err != nil {
		return err
	}
	c.mu.Lock()
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return err
	}
	if len(c.pending) >= 32 {
		c.mu.Unlock()
		return &RPCError{Code: -32001, Message: "Too many pending control requests.", Data: &RPCErrorData{Status: 503, Outcome: "not_admitted"}}
	}
	c.next++
	id := jsontext.Value(fmt.Sprintf("\"%d\"", c.next))
	reply := make(chan RPCMessage, 1)
	c.pending[string(id)] = reply
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, string(id)); c.mu.Unlock() }()
	data, err := json.Marshal(RPCMessage{JSONRPC: "2.0", ID: id, Method: method, Params: body})
	if err != nil {
		return err
	}
	if len(data) > RPCMessageLimit {
		return &RPCError{Code: -32602, Message: "Control request exceeds the message limit.", Data: &RPCErrorData{Status: 413, Outcome: "not_admitted"}}
	}
	select {
	case c.write <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	case <-c.ctx.Done():
		return errors.New("control connection closed")
	}
	if err := ctx.Err(); err != nil {
		<-c.write
		return err
	}
	writing, stop := context.WithTimeout(c.ctx, 10*time.Second)
	err = c.conn.Write(writing, websocket.MessageText, data)
	stop()
	<-c.write
	if err != nil {
		c.fail(err)
		return fmt.Errorf("control request outcome unknown; do not resubmit automatically: %w", err)
	}
	select {
	case response := <-reply:
		if response.Error != nil {
			return response.Error
		}
		if len(response.Result) == 0 {
			return errors.New("control response omitted its result; outcome unknown")
		}
		if result != nil {
			return json.Unmarshal(response.Result, result, nativeEncoding)
		}
		return nil
	case <-ctx.Done():
		if method != "request/cancel" {
			go func() {
				cancelCtx, cancel := context.WithTimeout(c.ctx, time.Second)
				defer cancel()
				_ = c.Call(cancelCtx, "request/cancel", RPCParams{Body: jsontext.Value(`{"id":` + string(id) + `}`)}, nil)
			}()
		}
		return fmt.Errorf("control request outcome unknown; do not resubmit automatically: %w", ctx.Err())
	case <-c.ctx.Done():
		return errors.New("control connection lost; request outcome unknown; do not resubmit automatically")
	}
}

// Subscribe registers delivery before sending its request, so publications
// arriving immediately after acknowledgement cannot race local registration.
func (c *RPCClient) Subscribe(ctx context.Context, method string, params RPCParams) (*RPCSubscription, error) {
	s := &RPCSubscription{client: c, ID: rand.Text(), ready: make(chan struct{}, 1), native: method == "view/attach" || method == "native/start"}
	c.mu.Lock()
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return nil, err
	}
	if len(c.subscriptions) >= 32 {
		c.mu.Unlock()
		return nil, errors.New("too many control subscriptions")
	}
	c.subscriptions[s.ID] = s
	c.mu.Unlock()
	params.Subscription = s.ID
	var response RPCSubscribed
	if err := c.Call(ctx, method, params, &response); err != nil {
		s.retire(err)
		go c.cancelSubscription(s.ID)
		return nil, err
	}
	if response.Subscription != s.ID {
		s.retire(errors.New("subscription acknowledgement changed identity"))
		c.Close()
		return nil, errors.New("invalid subscription acknowledgement")
	}
	return s, nil
}

func (s *RPCSubscription) retire(err error) {
	c := s.client
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.subscriptions[s.ID] == s {
		delete(c.subscriptions, s.ID)
		s.err = err
		close(s.ready)
	}
}

func (c *RPCClient) cancelSubscription(id string) {
	ctx, cancel := context.WithTimeout(c.ctx, time.Second)
	defer cancel()
	_ = c.Call(ctx, "subscription/cancel", RPCParams{Subscription: id}, nil)
}

func (s *RPCSubscription) Close() {
	s.retire(errors.New("subscription closed"))
	s.client.cancelSubscription(s.ID)
}

func (s *RPCSubscription) Next(ctx context.Context) (RPCEvent, error) {
	for {
		if err := ctx.Err(); err != nil {
			return RPCEvent{}, err
		}
		s.client.mu.Lock()
		if len(s.events) > 0 {
			event := s.events[0]
			s.events[0] = RPCEvent{}
			s.events = s.events[1:]
			s.bytes -= len(event.Data)
			s.client.mu.Unlock()
			return event, nil
		}
		err := s.err
		s.client.mu.Unlock()
		if err != nil {
			return RPCEvent{}, err
		}
		select {
		case <-s.ready:
		case <-ctx.Done():
			return RPCEvent{}, ctx.Err()
		}
	}
}
