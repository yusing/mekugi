package sessionapi

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"os"
	"strings"
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
	Inline bool    `json:"inline,omitzero"`
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

func (a Answer) validate() error {
	metadata, uploads := len(a.Call), 0
	for _, answer := range a.Answers {
		metadata += len(answer.Question) + len(answer.Text)
		for _, item := range answer.Attachments {
			metadata += len(item.Source) + len(item.Name) + len(item.Path)
			if item.Data != nil {
				data := *item.Data
				uploads += base64.StdEncoding.DecodedLen(len(data)) - (len(data) - len(strings.TrimRight(data, "=")))
			}
		}
	}
	if metadata > inputLimit || uploads > UploadLimit {
		return &InputError{Message: "Answers exceed the 1 MiB text/metadata or 32 MiB upload limit.", Limit: true}
	}
	return nil
}

// prepare keeps each question's snapshot separate while transferring ownership
// of the whole batch together. Refusal releases every unadmitted acquisition.
func (a *Answer) prepare(ctx context.Context, workspace string, prepare func(context.Context, string, InputRequest) (*PreparedInput, error)) (*PreparedInput, error) {
	if prepare == nil {
		return nil, errors.New("Attachment preparation is unavailable.")
	}
	var inputs []*PreparedInput
	defer func() {
		for _, input := range inputs {
			input.Cleanup()
		}
	}()
	for i := range a.Answers {
		answer := &a.Answers[i]
		if answer.Skip || len(answer.Attachments) == 0 {
			continue
		}
		input, err := prepare(ctx, workspace, InputRequest{Text: answer.Text, Attachments: answer.Attachments})
		if err != nil {
			return nil, err
		}
		inputs = append(inputs, input)
		draft := input.Draft.Clone()
		draft.Files, draft.Selections = nil, nil
		answer.draft = &draft
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return joinPreparedInputs(inputs), nil
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
