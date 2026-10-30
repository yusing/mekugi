package sessionapi

import (
	"context"
	"errors"

	"encoding/json/jsontext"

	"github.com/yusing/mekugi/modules/conversation/composer"
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
	var native NativeStart
	if err := decodeBody(body, &native); err != nil {
		return nil, err
	}
	_, subscription, err := h.admitRuntime(requestContext, viewContext, Start{Workspace: native.Workspace, Native: &native}, "", nil)
	if err != nil {
		return nil, runtimeStartFailure(err)
	}
	return subscription, nil
}

// RunNative admits an invocation without a renderer and submits one literal
// prompt through the existing runtime input owner. It joins the owned family.
func (h *Hub) RunNative(ctx context.Context, native NativeStart, prompt string) (string, error) {
	m, _, err := h.admitRuntime(ctx, nil, Start{Workspace: native.Workspace, Native: &native}, "", nil)
	if err != nil {
		return "", err
	}
	defer func() { m.cancel(); <-m.done }()
	id, err := m.waitReady(ctx)
	if err != nil {
		return "", err
	}
	prepared := NewPreparedInput(composer.Draft{Text: prompt}, nil)
	defer prepared.Cleanup()
	reply := make(chan string, 1)
	intent := Intent{Action: Send, Thread: id, Workspace: native.Workspace, Context: ctx, Prepared: prepared, Reply: reply}
	select {
	case m.server.intents <- intent:
	case <-m.done:
		return id, m.err
	case <-ctx.Done():
		return id, ctx.Err()
	}
	select {
	case refusal := <-reply:
		if refusal != "" {
			return id, errors.New(refusal)
		}
	case <-m.done:
		return id, m.err
	case <-ctx.Done():
		return id, ctx.Err()
	}
	select {
	case <-m.done:
		return id, m.err
	case <-ctx.Done():
		return id, ctx.Err()
	}
}
