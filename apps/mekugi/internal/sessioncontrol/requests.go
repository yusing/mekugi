// Package sessioncontrol owns application requests to the host. The host keeps
// execution, transport identities, and process lifetime authority.
package sessioncontrol

import (
	"encoding/json/jsontext"
	"sync"
	"sync/atomic"

	"github.com/yusing/mekugi/backends/codex/binding"

	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
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
	Method     string
	Scope      string
	Owner      T
	Side       *Side
	SideInput  *conversationmodel.Input
	Prediction *Prediction
	call       *HostCall
	input      *InputRequest
}

// Requests is the sole request ledger for one application client. Its event-loop
// owner serializes issuance and response delivery; it does not execute host work.
type Requests[T any] struct {
	ThreadPolicy *binding.ThreadAdmissionPolicy
	preparation  atomic.Pointer[PreparedThread]
	transport    Transport
	pending      map[string]Request[T]
	sides        map[string]*Side
	predictions  map[string]*Prediction
	calls        chan *HostCall
	closed       chan struct{}
	closeOnce    sync.Once
}

func NewRequests[T any](transport Transport) *Requests[T] {
	return &Requests[T]{transport: transport, pending: make(map[string]Request[T]), calls: make(chan *HostCall), closed: make(chan struct{})}
}

func (r *Requests[T]) Initialize(owner T) (string, error) {
	id, err := r.transport.Initialize()
	if err == nil {
		r.pending[id] = Request[T]{Method: "initialize", Owner: owner}
	}
	return id, err
}

func (r *Requests[T]) Send(method string, params any, scope string, owner T) (string, error) {
	if r.ThreadPolicy != nil && (method == "thread/start" || method == "thread/resume" || method == "thread/fork") {
		value, err := r.ThreadPolicy.Apply(params)
		if err != nil {
			return "", err
		}
		params = value
	}
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

// Lookup observes correlation without settling it while admission work is pending.
func (r *Requests[T]) Lookup(id string) (Request[T], bool) {
	request, ok := r.pending[id]
	return request, ok
}

// Attribute transfers response routing when a startup presentation acquires its
// first client. It preserves host identity and moves the observation lifetime.
func (r *Requests[T]) Attribute(id, scope string, owner T) bool {
	request, ok := r.pending[id]
	if !ok {
		return false
	}
	request.Owner = owner
	request.Scope = scope
	r.pending[id] = request
	return true
}

// Take consumes a response identity once. Unknown or retired identities have no
// owner and must not settle a newer operation with the same method.
func (r *Requests[T]) Take(id string) (Request[T], bool) {
	request, ok := r.pending[id]
	delete(r.pending, id)
	return request, ok
}

// Forget cancels observation, never the host operation.
func (r *Requests[T]) Forget(id string) {
	r.pending[id].retireCall()
	delete(r.pending, id)
}

func (r *Requests[T]) Retire(scope string) {
	for id, request := range r.pending {
		if request.Scope == scope {
			request.retireCall()
			delete(r.pending, id)
		}
	}
}

func (r *Requests[T]) Pending() bool { return len(r.pending) != 0 }

// PendingSideInputs borrows the input provenance still awaiting a host outcome.
// Main submissions remain with Conversation's submitted-input owner.
func (r *Requests[T]) PendingSideInputs() []conversationmodel.Input {
	var inputs []conversationmodel.Input
	for _, request := range r.pending {
		if request.SideInput != nil {
			inputs = append(inputs, *request.SideInput)
		}
	}
	return inputs
}
