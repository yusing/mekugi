package sessioncontrol

import (
	"context"
	"errors"

	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

type Inspection struct {
	thread string
	result chan inspectionResult
}

type inspectionResult struct {
	details control.SessionDetails
	err     error
}

// Target identifies the requested native thread. The owning event loop validates
// Main or one of its admitted side threads before publishing any facts.
func (request *Inspection) Target() string { return request.thread }

// Inspections remains readable while a user shell blocks mutating host calls.
func (r *Requests[T]) Inspections() <-chan *Inspection { return r.inspections }

// Inspect borrows current facts on the runtime's serialized event loop.
func (r *Requests[T]) Inspect(ctx context.Context, thread string) (control.SessionDetails, error) {
	request := &Inspection{thread: thread, result: make(chan inspectionResult, 1)}
	select {
	case r.inspections <- request:
	case <-ctx.Done():
		return control.SessionDetails{}, ctx.Err()
	case <-r.closed:
		return control.SessionDetails{}, errors.New("runtime inspection owner closed")
	}
	select {
	case result := <-request.result:
		return result.details, result.err
	case <-ctx.Done():
		return control.SessionDetails{}, ctx.Err()
	case <-r.closed:
		return control.SessionDetails{}, errors.New("runtime inspection owner closed")
	}
}

// Complete returns only facts belonging to the owning runtime.
func (request *Inspection) Complete(thread string, details control.SessionDetails) {
	result := inspectionResult{details: details}
	if request.thread != thread {
		result.details = control.SessionDetails{}
		result.err = errors.New("inspection target does not belong to this runtime")
	}
	request.result <- result
}
