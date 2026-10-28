package sessioncontrol

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"sync/atomic"

	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

const (
	callQueued uint32 = iota
	callDispatched
	callCanceled
)

// HostCall is an invocation-local handoff to the owning event loop. It carries no
// second request identity or ledger; the eventual host ID stays in Requests.
type HostCall struct {
	ctx           context.Context
	method, scope string
	params        jsontext.Value
	input         *control.NativeInput
	phase         atomic.Uint32
	result        chan hostCallResult
}

type hostCallResult struct {
	value jsontext.Value
	err   error
}

func (r *Requests[T]) Calls() <-chan *HostCall { return r.calls }

// call may run in an MCP handler. Cancellation before dispatch prevents the
// operation; cancellation after dispatch relinquishes observation, not execution.
func (r *Requests[T]) call(ctx context.Context, scope, method string, params any, input *control.NativeInput) (jsontext.Value, error) {
	raw, err := json.Marshal(params)
	if err != nil || scope == "" {
		return nil, &binding.DispatchError{Outcome: binding.NotDispatched, Method: method, Cause: errors.Join(errors.New("native operation requires a thread and valid parameters"), err)}
	}
	call := &HostCall{ctx: ctx, method: method, scope: scope, params: raw, input: input, result: make(chan hostCallResult, 1)}
	canceled := func(cause error) (jsontext.Value, error) {
		outcome := binding.Uncertain
		if call.phase.CompareAndSwap(callQueued, callCanceled) || call.phase.Load() == callCanceled {
			outcome = binding.NotDispatched
		}
		return nil, &binding.DispatchError{Outcome: outcome, Method: method, Cause: cause}
	}
	select {
	case <-ctx.Done():
		return canceled(ctx.Err())
	case <-r.closed:
		return canceled(errors.New("host request owner closed"))
	case r.calls <- call:
	}
	select {
	case result := <-call.result:
		return result.value, result.err
	case <-ctx.Done():
		// A settled reply wins over simultaneous caller cancellation.
		select {
		case result := <-call.result:
			return result.value, result.err
		default:
			return canceled(ctx.Err())
		}
	case <-r.closed:
		return canceled(errors.New("host request owner closed"))
	}
}

// NativeDelivery borrows the typed attribution retained by the sole host ledger.
// The consumer publishes it only after a successful correlated acknowledgement.
func (request Request[T]) NativeDelivery() *control.NativeInput {
	if request.call == nil || request.call.input == nil {
		return nil
	}
	input := request.call.input
	if input.Kind == "NEW_TASK" && input.Message != "" || !input.Run && (input.Kind == "MESSAGE" || input.Kind == "FINAL_ANSWER") {
		return new(*input)
	}
	return nil
}

// Dispatch runs only on the ledger's event-loop owner. Sending failures are
// uncertain because a transport may fail after writing part or all of a request.
func (r *Requests[T]) Dispatch(call *HostCall) {
	if call.ctx.Err() != nil {
		call.phase.CompareAndSwap(callQueued, callCanceled)
		return
	}
	if !call.phase.CompareAndSwap(callQueued, callDispatched) {
		return
	}
	var owner T
	id, err := r.Send(call.method, call.params, call.scope, owner)
	if err != nil {
		call.result <- hostCallResult{err: &binding.DispatchError{Outcome: binding.Uncertain, Method: call.method, Cause: err}}
		return
	}
	request := r.pending[id]
	request.call = call
	r.pending[id] = request
}

// CompleteCall consumes only queued feature requests, leaving ordinary UI and
// headless correlation with their existing consumers. Duplicate replies do nothing.
func (r *Requests[T]) CompleteCall(message appserver.Message) bool {
	if message.Method != "" {
		return false
	}
	request, ok := r.Lookup(string(message.ID))
	if !ok {
		return false
	}
	r.completeInput(string(message.ID), request, message)
	if request.call == nil {
		return false
	}
	r.Take(string(message.ID))
	result := hostCallResult{value: bytes.Clone(message.Result)}
	if message.Error != nil {
		result.err = &binding.DispatchError{Outcome: binding.HostRejected, Method: request.Method, Cause: errors.New(message.Error.Message)}
	}
	request.call.result <- result
	return true
}

// Close releases call observations. It neither interrupts nor reissues host work.
func (r *Requests[T]) Close() { r.closeOnce.Do(func() { close(r.closed) }) }

func (request Request[T]) retireCall() {
	if request.input != nil {
		request.input.Settled("", &binding.DispatchError{Outcome: binding.Uncertain, Method: request.Method, Cause: errors.New("host input observation retired")})
	}
	if request.call != nil {
		request.call.result <- hostCallResult{err: &binding.DispatchError{Outcome: binding.Uncertain, Method: request.Method, Cause: errors.New("host request observation retired")}}
	}
}
