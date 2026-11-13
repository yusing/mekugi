package sessioncontrol

import (
	"context"
	"errors"

	"github.com/yusing/mekugi/contracts/observation"

	"github.com/yusing/mekugi/backends/codex/binding"
	conversationbinding "github.com/yusing/mekugi/modules/conversation/codexbinding"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

type Inspection struct {
	measurement *HostCall
	thread      string
	result      chan inspectionResult
	window      string
	loaded      bool
	attachments []composer.ContextAttachment
}

type inspectionResult struct {
	details     observation.SessionDetails
	err         error
	attachments composer.AttachmentSnapshot
	cached      bool
}

// AttachmentReader borrows the same admitted runtime and native history used by
// ordinary thread restoration. It neither opens source files nor runs skills.
type AttachmentReader interface {
	ContextAttachments(context.Context, string, string) (composer.AttachmentSnapshot, error)
}

func (r *Requests[T]) ContextAttachments(ctx context.Context, thread, window string) (composer.AttachmentSnapshot, error) {
	query := &Inspection{thread: thread, window: window, result: make(chan inspectionResult, 1)}
	result := r.inspect(ctx, query)
	if result.err != nil || result.cached {
		return result.attachments, result.err
	}
	turns, err := r.attachmentHistory(ctx, thread)
	if err != nil {
		return composer.AttachmentSnapshot{}, err
	}
	query = &Inspection{thread: thread, window: window, loaded: true, attachments: conversationbinding.CompactedAttachments(turns), result: make(chan inspectionResult, 1)}
	result = r.inspect(ctx, query)
	return result.attachments, result.err
}

func (r *Requests[T]) attachmentHistory(ctx context.Context, thread string) ([]binding.HistoryTurn, error) {
	raw, err := r.call(ctx, thread, "thread/read", map[string]any{"threadId": thread, "includeTurns": true})
	if err != nil {
		return nil, err
	}
	info, err := binding.DecodeHistoryThread(raw, thread)
	return info.Turns, err
}

func (request *Inspection) ContextWindow() string { return request.window }

func (request *Inspection) CompleteAttachments(thread string, cache *composer.AttachmentContext) {
	if request.thread != thread {
		request.Complete(thread, observation.SessionDetails{})
		return
	}
	if request.loaded {
		cache.Set(request.window, request.attachments)
	}
	values, cached := cache.Get(request.window)
	request.result <- inspectionResult{attachments: values, cached: cached}
}

// Target identifies the requested native thread. The owning event loop validates
// Main or one of its admitted side threads before publishing any facts.
func (request *Inspection) Target() string { return request.thread }

// Inspections remains readable while a user shell blocks mutating host calls.
func (r *Requests[T]) Inspections() <-chan *Inspection { return r.inspections }

// DispatchMeasurement keeps observational RPCs on the existing ledger while
// mutating session requests remain gated by approvals or auxiliary execution.
func (r *Requests[T]) DispatchMeasurement(request *Inspection) bool {
	if request.measurement == nil {
		return false
	}
	r.Dispatch(request.measurement)
	return true
}

// Inspect borrows current facts on the runtime's serialized event loop.
func (r *Requests[T]) Inspect(ctx context.Context, thread string) (observation.SessionDetails, error) {
	request := &Inspection{thread: thread, result: make(chan inspectionResult, 1)}
	result := r.inspect(ctx, request)
	return result.details, result.err
}

func (r *Requests[T]) inspect(ctx context.Context, request *Inspection) inspectionResult {
	select {
	case r.inspections <- request:
	case <-ctx.Done():
		return inspectionResult{err: ctx.Err()}
	case <-r.closed:
		return inspectionResult{err: errors.New("runtime inspection owner closed")}
	}
	select {
	case result := <-request.result:
		return result
	case <-ctx.Done():
		return inspectionResult{err: ctx.Err()}
	case <-r.closed:
		return inspectionResult{err: errors.New("runtime inspection owner closed")}
	}
}

// Complete returns only facts belonging to the owning runtime.
func (request *Inspection) Complete(thread string, details observation.SessionDetails) {
	result := inspectionResult{details: details}
	if request.thread != thread {
		result.details = observation.SessionDetails{}
		result.err = errors.New("inspection target does not belong to this runtime")
	}
	request.result <- result
}
