package sessioncontrol

import (
	json "encoding/json/v2"
	"errors"
	"fmt"

	"github.com/yusing/mekugi/backends/codex/appserver"
)

// StopProcess records the human cancellation independently of host termination.
// Neither history injection nor termination starts a model turn.
func (r *Requests[T]) StopProcess(thread, process string, owner T) error {
	noticeErr := r.processNotice(thread, processCancellationNotice(process), owner)
	id, stopErr := r.Send("thread/backgroundTerminals/terminate", map[string]any{"threadId": thread, "processId": process}, thread, owner)
	if stopErr == nil {
		request := r.pending[id]
		request.process = process
		r.pending[id] = request
	} else {
		noticeErr = errors.Join(noticeErr, r.processNotice(thread, processTerminationFailureNotice(process, stopErr.Error()), owner))
		stopErr = fmt.Errorf("could not send process termination: %w", stopErr)
	}
	return errors.Join(noticeErr, stopErr)
}

func (r *Requests[T]) processNotice(thread, text string, owner T) error {
	item := map[string]any{"type": "message", "role": "developer", "content": []any{map[string]any{"type": "input_text", "text": text}}}
	id, err := r.Send("thread/inject_items", map[string]any{"threadId": thread, "items": []any{item}}, thread, owner)
	if err != nil {
		return fmt.Errorf("could not deliver process cancellation notice: %w", err)
	}
	request := r.pending[id]
	request.processNotice = true
	r.pending[id] = request
	return nil
}

// ProcessResponse settles cancellation delivery even if its requesting view has
// detached. The caller consumes the existing request ledger entry once.
func (r *Requests[T]) ProcessResponse(request Request[T], message appserver.Message) (bool, string) {
	if request.processNotice {
		if message.Error != nil {
			return true, "Could not deliver process cancellation notice: " + message.Error.Message
		}
		return true, ""
	}
	if request.process == "" {
		return false, ""
	}
	var result struct {
		Terminated bool `json:"terminated"`
	}
	detail := ""
	if message.Error != nil {
		detail = message.Error.Message
	} else if err := json.Unmarshal(message.Result, &result); err != nil {
		detail = "invalid host termination response"
	} else if !result.Terminated {
		detail = "the process already ended or could not be stopped"
	}
	if detail == "" {
		return true, ""
	}
	noticeErr := r.processNotice(request.Scope, processTerminationFailureNotice(request.process, detail), request.Owner)
	warning := "Could not stop process: " + detail
	if noticeErr != nil {
		warning += "; " + noticeErr.Error()
	}
	return true, warning
}
