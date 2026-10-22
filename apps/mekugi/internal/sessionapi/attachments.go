package sessionapi

import (
	"context"
	"errors"
	"net/http"
	"os"
	"sync"

	"github.com/yusing/mekugi/modules/conversation/composer"
)

// UploadLimit bounds the combined decoded upload bytes in one input.
const UploadLimit = 32 << 20

// InputBodyLimit includes base64 expansion plus the ordinary text/metadata budget.
const InputBodyLimit = (UploadLimit+2)/3*4 + inputLimit

type Attachment struct {
	Source string  `json:"source"`
	Path   string  `json:"path,omitempty"`
	Name   string  `json:"name,omitempty"`
	Data   *string `json:"data,omitzero"`
}

type InputRequest struct {
	Text        string       `json:"text"`
	Attachments []Attachment `json:"attachments,omitempty"`
	// NativeDraft is supplied by the private native transport after decoding its
	// semantic composer request. Ordinary JSON input cannot set it.
	NativeDraft *composer.Draft `json:"-"`
}

// InputError is a permanent preparation error, not a retryable admission refusal.
type InputError struct {
	Message string
	Limit   bool
}

func (e *InputError) Error() string { return e.Message }

func inputError(err error) *RPCError {
	status := http.StatusInternalServerError
	if failure, ok := errors.AsType[*InputError](err); ok {
		status = http.StatusBadRequest
		if failure.Limit {
			status = http.StatusRequestEntityTooLarge
		}
	} else if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		status = http.StatusServiceUnavailable
	}
	return rpcFailure(status, err.Error())
}

// acquireInput bounds only active acquisition. Admission and semantic control
// never retain this capacity while waiting for the execution owner.
func acquireInput(preparations chan struct{}, prepare func() (*PreparedInput, error)) (*PreparedInput, *RPCError) {
	select {
	case preparations <- struct{}{}:
	default:
		return nil, rpcFailure(503, "Attachment preparation capacity is busy.")
	}
	defer func() { <-preparations }()
	prepared, err := prepare()
	if err != nil {
		return nil, inputError(err)
	}
	return prepared, nil
}

// PreparedInput holds one immutable snapshot and the request's acquired images.
// Admission and cancellation race only at this ownership transfer, never while
// mutating editors or queued inputs. User paths are never included in acquired.
type PreparedInput struct {
	Draft composer.Draft

	mu       sync.Mutex
	acquired []string
	finished bool
	admitted bool
}

func NewPreparedInput(draft composer.Draft, acquired []string) *PreparedInput {
	return &PreparedInput{Draft: draft, acquired: acquired}
}

// Admit transfers acquired files to the existing queue's image owner once.
func (p *PreparedInput) Admit(ctx context.Context, owned map[string]bool) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.finished || ctx != nil && ctx.Err() != nil {
		return false
	}
	for _, path := range p.acquired {
		owned[path] = true
	}
	p.acquired = nil
	p.finished = true
	p.admitted = true
	return true
}

// Admitted reports whether the queue has already taken ownership.
func (p *PreparedInput) Admitted() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.admitted
}

// Cleanup releases only images still owned by this request. It is safe after
// admission, cancellation or a late owner-loop completion.
func (p *PreparedInput) Cleanup() {
	if p == nil {
		return
	}
	p.mu.Lock()
	acquired := p.acquired
	p.acquired = nil
	p.finished = true
	p.mu.Unlock()
	// Filesystem cleanup must not hold the admission lock or block the owner.
	for _, path := range acquired {
		_ = os.Remove(path)
	}
}

// joinPreparedInputs combines request-owned acquisitions before publishing any
// admission capability. Children are consumed; one owner admits the whole batch.
func joinPreparedInputs(inputs []*PreparedInput) *PreparedInput {
	batch := NewPreparedInput(composer.Draft{}, nil)
	for _, input := range inputs {
		input.mu.Lock()
		batch.acquired = append(batch.acquired, input.acquired...)
		input.acquired = nil
		input.finished = true
		input.mu.Unlock()
	}
	return batch
}
