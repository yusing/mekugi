package sessionapi

import (
	"context"
	"errors"
	"sync"
)

// Descendants reads only runtimes owned below the selected parent's lifetime.
func (h *Hub) Descendants(parent string) map[string]Frame {
	h.mu.Lock()
	result := make(map[string]Frame)
	root := h.sessions[parent]
	if root == nil || root.runtime == nil {
		h.mu.Unlock()
		return result
	}
	servers := make(map[string]*Server)
	for id, child := range h.sessions {
		for owner := child.runtime; owner != nil && owner.parent != nil; owner = owner.parent {
			if owner.parent != root.runtime {
				continue
			}
			servers[id] = child.server
			break
		}
	}
	h.mu.Unlock()
	for id, s := range servers {
		s.mu.Lock()
		if s.published {
			result[id] = s.frame
		}
		s.mu.Unlock()
	}
	return result
}

// Observation borrows a native view and admits only observational operations.
// Its latest frame is the same detached subscription value used by native clients.
type Observation struct {
	mu       sync.Mutex
	frame    NativeEvent
	err      error
	requests chan NativeOperation
	ctx      context.Context
	cancel   context.CancelFunc
}

func (o *Observation) Close() {
	if o != nil {
		o.cancel()
	}
}
func (o *Observation) Snapshot() (NativeEvent, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.frame, o.err
}

func (o *Observation) Read(operation NativeOperation) error {
	if o.ctx.Err() != nil {
		return errors.New("The child observation ended.")
	}
	if !operation.valid() || operation.Output == nil && operation.Details == nil && operation.CloseDetails == nil && operation.DetailOutput == nil {
		return errors.New("Child observation accepts only output and detail reads.")
	}
	select {
	case o.requests <- operation:
		return nil
	default:
		return errors.New("The child observation is busy; retry the read.")
	}
}

// Observe never opens or resumes execution. Losing this view cannot stop it.
func (h *Hub) Observe(ctx context.Context, parent, thread string) (*Observation, error) {
	if _, ok := h.Descendants(parent)[thread]; !ok {
		return nil, errors.New("The child runtime is unavailable.")
	}
	h.mu.Lock()
	child := h.sessions[thread]
	workspace := ""
	if child != nil {
		workspace = child.entry.Workspace
	}
	h.mu.Unlock()
	if child == nil {
		return nil, errors.New("The child runtime ended.")
	}
	ctx, cancel := context.WithCancel(ctx)
	n := child.server.Native()
	sub, request, failure := n.newView(ctx, requestTarget{thread, workspace}, "", localNativeView)
	if failure != nil {
		cancel()
		return nil, failure
	}
	o := &Observation{requests: make(chan NativeOperation, 8), cancel: cancel, ctx: ctx}
	go func() {
		defer cancel()
		defer sub.close()
		setError := func(err error) { o.mu.Lock(); o.err = err; o.mu.Unlock() }
		if failure := n.deliver(request); failure != nil {
			setError(failure)
			return
		}
		ready := false
		for {
			var requests <-chan NativeOperation
			if ready {
				requests = o.requests
			}
			select {
			case <-ctx.Done():
				return
			case <-sub.ctx.Done():
				setError(errors.New("The child observation closed before its final publication."))
				return
			case <-sub.native.ready:
				event, ok := sub.native.pop()
				if !ok {
					continue
				}
				if event.Kind == "exit" {
					return
				}
				previous, _ := o.Snapshot()
				frame, err := decodeNativeEvent(event.Kind, event.Data, previous)
				if err != nil {
					setError(err)
					return
				}
				o.mu.Lock()
				o.frame = frame
				ready = true
				o.mu.Unlock()
			case operation := <-requests:
				frame, _ := o.Snapshot()
				read := request
				read.Reply = make(chan string, 1)
				read.Attach, read.Version, read.Operation = false, frame.Frame.View.Version, operation
				if failure := n.deliver(read); failure != nil {
					setError(failure)
				} else {
					setError(nil)
				}
			}
		}
	}()
	return o, nil
}
