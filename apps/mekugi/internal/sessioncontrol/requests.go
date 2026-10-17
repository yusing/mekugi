// Package sessioncontrol owns application requests to the host. The host keeps
// execution, transport identities, and process lifetime authority.
package sessioncontrol

import (
	"encoding/json/jsontext"
)

// Responder preserves the host-owned identity of an admitted server request.
type Responder interface {
	Respond(id jsontext.Value, result any) error
}

// Transport is the app-server's existing request and response surface.
type Transport interface {
	Responder
	Initialize() (string, error)
	Send(method string, params any, request bool) (string, error)
}

// Request retains only routing context. Scope identifies operations retired
// together when their owning session is replaced; Owner stays with the consumer.
type Request[T any] struct {
	Method string
	Scope  string
	Owner  T
}

// Requests is the sole request ledger for one application client. Its event-loop
// owner serializes issuance and response delivery; it does not execute host work.
type Requests[T any] struct {
	transport Transport
	pending   map[string]Request[T]
	sides     map[string]T
}

func NewRequests[T any](transport Transport) *Requests[T] {
	return &Requests[T]{transport: transport, pending: make(map[string]Request[T])}
}

func (r *Requests[T]) Initialize(owner T) (string, error) {
	id, err := r.transport.Initialize()
	if err == nil {
		r.pending[id] = Request[T]{Method: "initialize", Owner: owner}
	}
	return id, err
}

func (r *Requests[T]) Send(method string, params any, scope string, owner T) (string, error) {
	id, err := r.transport.Send(method, params, true)
	if err == nil {
		r.pending[id] = Request[T]{Method: method, Scope: scope, Owner: owner}
	}
	return id, err
}

func (r *Requests[T]) Notify(method string, params any) error {
	_, err := r.transport.Send(method, params, false)
	return err
}

func (r *Requests[T]) Respond(id jsontext.Value, result any) error {
	return r.transport.Respond(id, result)
}

// Take consumes a response identity once. Unknown or retired identities have no
// owner and must not settle a newer operation with the same method.
func (r *Requests[T]) Take(id string) (Request[T], bool) {
	request, ok := r.pending[id]
	delete(r.pending, id)
	return request, ok
}

// Forget cancels observation, never the host operation.
func (r *Requests[T]) Forget(id string) { delete(r.pending, id) }

func (r *Requests[T]) Retire(scope string) {
	for id, request := range r.pending {
		if request.Scope == scope {
			delete(r.pending, id)
		}
	}
}

func (r *Requests[T]) Pending() bool { return len(r.pending) != 0 }
