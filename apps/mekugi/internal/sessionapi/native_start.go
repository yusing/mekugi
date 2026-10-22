package sessionapi

import (
	"context"
	"crypto/rand"
	"encoding/json/jsontext"
	"os"
	"path/filepath"
)

// NativeStart preserves native invocation arguments on the private socket only.
// A startup operation is not a thread identity. It hosts welcome/resume selection
// before the execution owner confirms a logical thread.
type NativeStart struct {
	Workspace  string   `json:"workspace"`
	RouterArgs []string `json:"routerArgs"`
	Args       []string `json:"args"`
}

func (h *Hub) startNative(requestContext, viewContext context.Context, body jsontext.Value) (*rpcSubscription, *RPCError) {
	if h.launcher == nil {
		return nil, rpcFailure(405, "This server does not start threads.")
	}
	var native NativeStart
	if err := decodeBody(body, &native); err != nil {
		return nil, err
	}
	info, err := os.Stat(native.Workspace)
	if !filepath.IsAbs(native.Workspace) || err != nil || !info.IsDir() {
		return nil, rpcFailure(400, "Choose an existing absolute workspace directory.")
	}
	native.Workspace = filepath.Clean(native.Workspace)
	h.mu.Lock()
	if h.stopping || h.ctx.Err() != nil || requestContext.Err() != nil {
		h.mu.Unlock()
		return nil, rpcFailure(503, "The server is stopping.")
	}
	ctx, cancel := context.WithCancel(h.ctx)
	m := &managedSession{operation: rand.Text(), ctx: ctx, server: NewPreparedLive(h.launcher.PrepareInput), inputContext: ctx, inputWorkspace: native.Workspace, cancel: cancel, done: make(chan struct{}), ready: make(chan string, 1)}
	m.server.activeThread = h.activeThread
	n := m.server.Native()
	// The initial observer exists before Run, including immediate failures.
	subscription, attach, failure := n.newView(viewContext, requestTarget{workspace: native.Workspace}, m.operation, localNativeView)
	if failure != nil {
		h.mu.Unlock()
		cancel()
		return nil, failure
	}
	h.running[m] = struct{}{}
	h.wg.Add(1)
	h.mu.Unlock()
	go h.run(ctx, m, Start{Workspace: native.Workspace, Native: &native})
	attach.Context = requestContext
	if failure = n.deliver(attach); failure != nil {
		subscription.close()
		return nil, failure
	}
	return subscription, nil
}
