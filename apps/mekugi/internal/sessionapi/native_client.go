package sessionapi

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
)

type nativeRPCScope struct {
	mu   sync.Mutex
	refs int
	rpc  *RPCClient
}

// NativeConnection owns a WebSocket subscription through the private control socket.
// Closing it releases its stream and presentation scope, never the execution.
// It does not reconnect or retry mutations with potentially ambiguous outcomes.
type NativeConnection struct {
	scope        *nativeRPCScope
	closeOnce    sync.Once
	client       *http.Client
	base         string
	thread       string
	startup      string
	subscription *RPCSubscription
	last         NativeEvent
}

// NativeFailure preserves ordinary admission refusal separately from unknown
// transport outcomes. Its text is suitable for the native feedback surface.
type NativeFailure struct {
	Status int
	Reason string
}

func (e *NativeFailure) Error() string { return e.Reason }

func OpenNative(ctx context.Context, client *http.Client, base, thread string) (*NativeConnection, error) {
	if thread == "" {
		return nil, errors.New("native attachment requires a logical thread identity")
	}
	return openNativeSubscription(ctx, client, base, "view/attach", RPCParams{Thread: thread})
}

// Attach opens another view on this same admitted server connection. The caller
// keeps the source stream until the destination's first publication is received.
func (c *NativeConnection) Attach(ctx context.Context, thread string) (*NativeConnection, error) {
	if thread == "" {
		return nil, errors.New("native attachment requires a logical thread identity")
	}
	c.scope.mu.Lock()
	if c.scope.refs == 0 {
		c.scope.mu.Unlock()
		return nil, errors.New("native connection closed")
	}
	c.scope.refs++
	c.scope.mu.Unlock()
	subscription, err := c.scope.rpc.Subscribe(ctx, "view/attach", RPCParams{Thread: thread})
	if err != nil {
		c.scope.release()
		return nil, nativeRPCFailure(err)
	}
	return &NativeConnection{client: c.client, base: c.base, thread: thread, scope: c.scope, subscription: subscription}, nil
}

func (s *nativeRPCScope) release() {
	s.mu.Lock()
	s.refs--
	last := s.refs == 0
	s.mu.Unlock()
	if last {
		s.rpc.Close()
	}
}

func openNativeSubscription(ctx context.Context, client *http.Client, base, method string, params RPCParams) (*NativeConnection, error) {
	if client == nil {
		return nil, errors.New("native attachment requires a control client")
	}
	rpc, err := OpenRPC(ctx, client, strings.TrimRight(base, "/")+NativeRPCPath, "")
	if err != nil {
		return nil, nativeRPCFailure(err)
	}
	subscription, err := rpc.Subscribe(ctx, method, params)
	if err != nil {
		rpc.Close()
		return nil, nativeRPCFailure(err)
	}
	return &NativeConnection{client: client, base: strings.TrimRight(base, "/"), thread: params.Thread, startup: params.Startup, scope: &nativeRPCScope{rpc: rpc, refs: 1}, subscription: subscription}, nil
}

func (c *NativeConnection) Close() error {
	c.closeOnce.Do(func() { c.subscription.Close(); c.scope.release() })
	return nil
}

func nativeRPCFailure(err error) error {
	if failure, ok := errors.AsType[*RPCError](err); ok && failure.Data != nil && failure.Data.Outcome == "not_admitted" {
		return &NativeFailure{Status: failure.Data.Status, Reason: failure.Message}
	}
	return err
}

// Next consumes a snapshot or a complete-value native patch. It must be called
// by one stream reader. Mutations separately retain their originating Target.
func (c *NativeConnection) Next() (NativeEvent, error) {
	publication, err := c.subscription.Next(context.Background())
	if err != nil {
		return NativeEvent{}, err
	}
	kind, data := publication.Kind, publication.Data
	if kind == "exit" {
		var outcome NativeExit
		if err := json.Unmarshal(data, &outcome); err != nil {
			return NativeEvent{}, fmt.Errorf("decode native exit: %w", err)
		}
		return NativeEvent{}, &outcome
	}
	first := c.last.Attachment == ""
	if kind != "snapshot" && kind != "patch" || first && kind != "snapshot" {
		return NativeEvent{}, errors.New("native stream did not provide a compatible snapshot")
	}
	var event NativeEvent
	if err := json.Unmarshal(data, &event, nativeEncoding); err != nil {
		return NativeEvent{}, fmt.Errorf("decode native publication: %w", err)
	}
	if event.Attachment == "" || c.last.Attachment != "" && c.last.Attachment != event.Attachment {
		return NativeEvent{}, errors.New("native stream changed its presentation attachment")
	}
	if first && c.thread == "" && c.startup == "" {
		c.startup = event.Startup
	}
	if replacement := event.Frame.Switch; replacement != nil {
		if replacement.Origin != event.Attachment || replacement.From != c.thread || replacement.To != event.Frame.View.Main.ID {
			return NativeEvent{}, errors.New("native stream supplied an invalid thread replacement")
		}
		c.thread, c.startup = replacement.To, ""
	} else if event.Frame.View.Main.ID != c.thread || event.Startup != c.startup {
		return NativeEvent{}, errors.New("native stream silently changed its logical thread")
	}
	c.last = event
	return event, nil
}

// NativeResult identifies the publication settled before request acknowledgement.
// Key continuations wait for that version rather than interpreting stale menus.
type NativeResult struct {
	Version sessionpresentation.Version `json:"version"`
}

func (c *NativeConnection) Send(ctx context.Context, target NativeTarget, operation NativeOperation) (NativeResult, error) {
	if target.Attachment == "" || target.Thread == "" && target.Startup == "" {
		return NativeResult{}, errors.New("wait for the native attachment snapshot")
	}
	if !operation.valid() {
		return NativeResult{}, errors.New("exactly one semantic native operation is required")
	}
	mutation := NativeMutation{Attachment: target.Attachment, Version: target.Version, Operation: operation}
	data, err := json.Marshal(mutation, nativeEncoding)
	if err != nil {
		return NativeResult{}, err
	}
	rpc := c.scope.rpc
	// Large/acquired input cannot occupy the control connection ahead of an
	// interrupt. It borrows the originating view's identity and lifetime.
	if operation.Composer != nil && (len(operation.Composer.Draft.Files) > 0 || len(operation.Composer.Draft.Attachments) > 0 || operation.Composer.Input != nil) || operation.Answers != nil || len(data) > 64<<10 {
		rpc, err = OpenRPC(ctx, c.client, c.base+NativeRPCPath, "")
		if err != nil {
			return NativeResult{}, nativeRPCFailure(err)
		}
		defer rpc.Close()
	}
	var result NativeResult
	err = rpc.Call(ctx, "view/intent", RPCParams{Thread: target.Thread, Startup: target.Startup, Body: data}, &result)
	return result, nativeRPCFailure(err)
}

// StartNative admits one invocation operation, including its welcome or resume
// chooser before a thread exists. The request is never automatically replayed.
func StartNative(ctx context.Context, client *http.Client, base string, start NativeStart) (*NativeConnection, error) {
	data, err := json.Marshal(start)
	if err != nil {
		return nil, err
	}
	return openNativeSubscription(ctx, client, base, "native/start", RPCParams{Body: data})
}
