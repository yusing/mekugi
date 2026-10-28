package sessioncontrol

import (
	"context"
	"errors"
	"slices"
	"sync"

	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/modules/conversation/prompt"
)

// DelegateApprovals carries native runtime decisions to an existing approval
// dock without adopting another backend's wire request identity.
type DelegateApprovals struct {
	ctx      context.Context
	cancel   context.CancelFunc
	requests chan *DelegateApprovalRequest
}

func NewDelegateApprovals(ctx context.Context) *DelegateApprovals {
	ctx, cancel := context.WithCancel(ctx)
	return &DelegateApprovals{ctx: ctx, cancel: cancel, requests: make(chan *DelegateApprovalRequest)}
}

func (a *DelegateApprovals) Requests() <-chan *DelegateApprovalRequest {
	if a == nil {
		return nil
	}
	return a.requests
}

func (a *DelegateApprovals) Close() {
	if a != nil {
		a.cancel()
	}
}

// Request ends only on a decision or cancellation of the request or dock owner.
// The prompt is detached before the UI assigns its local identity/editor state.
func (a *DelegateApprovals) Request(ctx context.Context, source observation.Thread, approval *prompt.Approval) (prompt.Choice, string, error) {
	if a == nil || approval == nil {
		return prompt.Choice{}, "", errors.New("approval request has no live owner or prompt")
	}
	if source.ID == "" || source.ID != approval.Thread {
		return prompt.Choice{}, "", errors.New("approval source does not match the native thread")
	}
	copy := *approval
	copy.Details, copy.Choices = slices.Clone(approval.Details), slices.Clone(approval.Choices)
	done := make(chan struct{})
	request := &DelegateApprovalRequest{Approval: &copy, Source: source, Done: done, done: done, ctx: ctx, owner: a.ctx}
	select {
	case a.requests <- request:
	case <-ctx.Done():
		request.finish(prompt.Choice{}, "", ctx.Err())
	case <-a.ctx.Done():
		request.finish(prompt.Choice{}, "", a.ctx.Err())
	}
	select {
	case <-request.Done:
	case <-ctx.Done():
		request.finish(prompt.Choice{}, "", ctx.Err())
	case <-a.ctx.Done():
		request.finish(prompt.Choice{}, "", a.ctx.Err())
	}
	return request.choice, request.reason, request.err
}

// DelegateApprovalRequest is live response authority, never a published view.
type DelegateApprovalRequest struct {
	Approval *prompt.Approval
	Source   observation.Thread
	Done     <-chan struct{}
	ctx      context.Context
	owner    context.Context
	done     chan struct{}
	mu       sync.Mutex
	choice   prompt.Choice
	reason   string
	err      error
}

func (r *DelegateApprovalRequest) Finished() bool {
	if r.ctx.Err() != nil || r.owner.Err() != nil {
		return true
	}
	select {
	case <-r.Done:
		return true
	default:
		return false
	}
}

// Reply arbitrates with cancellation and other replies exactly once.
func (r *DelegateApprovalRequest) Reply(choice prompt.Choice, reason string) bool {
	return r.finish(choice, reason, nil)
}

func (r *DelegateApprovalRequest) finish(choice prompt.Choice, reason string, err error) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	select {
	case <-r.Done:
		return false
	default:
	}
	if err == nil {
		err = r.ctx.Err()
	}
	if err == nil {
		err = r.owner.Err()
	}
	if err != nil {
		choice, reason = prompt.Choice{}, ""
	}
	r.choice, r.reason, r.err = choice, reason, err
	close(r.done)
	return err == nil
}
