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
func (r *Requests[T]) call(ctx context.Context, scope, method string, params any) (jsontext.Value, error) {
	raw, err := json.Marshal(params)
	if err != nil || scope == "" {
		return nil, &binding.DispatchError{Outcome: binding.NotDispatched, Method: method, Cause: errors.Join(errors.New("native operation requires a thread and valid parameters"), err)}
	}
	call := &HostCall{ctx: ctx, method: method, scope: scope, params: raw, result: make(chan hostCallResult, 1)}
	canceled := func(cause error) (jsontext.Value, error) {
		outcome := binding.Uncertain
		if call.phase.CompareAndSwap(callQueued, callCanceled) || call.phase.Load() == callCanceled {
			outcome = binding.NotDispatched
		}
		return nil, &binding.DispatchError{Outcome: outcome, Method: method, Cause: cause}
	}
	calls := r.calls
	var inspections chan *Inspection
	if method == "featureUse/read" || method == "agent/observe" || method == "agent/observeTree" || method == "agent/foreign/receive" {
		calls, inspections = nil, r.inspections
	}
	select {
	case <-ctx.Done():
		return canceled(ctx.Err())
	case <-r.closed:
		return canceled(errors.New("host request owner closed"))
	case calls <- call:
	case inspections <- &Inspection{measurement: call}:
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

func (r *Requests[T]) ExternalForeignReceive(ctx context.Context, root string, request appserver.AgentForeignRequest) (appserver.AgentForeignResponse, error) {
	var response appserver.AgentForeignResponse
	raw, err := r.call(ctx, root, "agent/foreign/receive", request)
	if err == nil {
		err = json.Unmarshal(raw, &response)
	}
	return response, err
}

// Dispatch runs only on the ledger's event-loop owner. Sending failures are
// uncertain because a transport may fail after writing part or all of a request.
func (r *Requests[T]) Dispatch(call *HostCall) bool {
	if call.ctx.Err() != nil {
		call.phase.CompareAndSwap(callQueued, callCanceled)
		return false
	}
	if !call.phase.CompareAndSwap(callQueued, callDispatched) {
		return false
	}
	var owner T
	id, err := r.Send(call.method, call.params, call.scope, owner)
	if err != nil {
		call.result <- hostCallResult{err: &binding.DispatchError{Outcome: binding.Uncertain, Method: call.method, Cause: err}}
		return false
	}
	request := r.pending[id]
	request.call = call
	r.pending[id] = request
	return true
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
