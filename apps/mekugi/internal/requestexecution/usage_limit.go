package requestexecution

import (
	"bytes"
	"crypto/rand"
	"io"
	"net/http"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

// usageLimitResponse keeps a definite provider rejection out of host failure
// history. The logical wait owns resumption, not the HTTP/socket lifetime.
type usageLimitResponse struct{ attempt *requestAttempt }

func (a *requestAttempt) completeUsageLimit() (bool, error) {
	if a.streamResponse || a.response.StatusCode < http.StatusBadRequest {
		return false, nil
	}
	body := a.response.Body
	payload, err := io.ReadAll(io.LimitReader(body, 8193))
	a.response.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(payload), body), body}
	if err != nil || len(payload) > 8192 {
		return false, err
	}
	stop, err := (usageLimitResponse{a}).pause(payload)
	if stop == nil || err != nil {
		return false, err
	}
	defer body.Close()
	return true, a.writeStoppedWait(stop)
}

func (t usageLimitResponse) pause(payload []byte) (*control.LocalStop, error) {
	a := t.attempt
	if a.executor.ThreadControl == nil || a.approvalReview || a.prewarm || !a.metadataValid || a.metadata.RequestKind != responses.Turn {
		return nil, nil
	}
	deadline, limited, capacity := inferencegateway.ProviderWait(payload, a.response.Header, time.Now())
	if !limited {
		return nil, nil
	}
	var stop *control.LocalStop
	var err error
	if capacity {
		stop, err = a.executor.ThreadControl.WaitForCapacity(a.executionCtx, a.threadID, a.metadata.TurnID, deadline)
	} else {
		stop, err = a.executor.ThreadControl.WaitForUsageLimit(a.executionCtx, a.threadID, a.metadata.TurnID, deadline)
	}
	if err != nil || stop == nil {
		return nil, err
	}
	a.usageLimited, a.finishUsage = true, false
	a.finalization.providerFailure = nil
	return stop, nil
}

func (t usageLimitResponse) TransformJSON(payload []byte) ([]byte, error) {
	stop, err := t.pause(payload)
	if stop == nil || err != nil {
		return payload, err
	}
	a := t.attempt
	a.response.StatusCode = http.StatusOK
	a.response.Header.Set("Content-Type", "application/json")
	a.response.Header.Del("Content-Length")
	return responses.MustMarshalJSON(map[string]any{"id": "mekugi_wait_" + rand.Text(), "object": "response", "status": "completed", "output": []any{}}), nil
}

func (t usageLimitResponse) TransformSSE(payload []byte) ([][]byte, error) {
	stop, err := t.pause(payload)
	if stop == nil || err != nil {
		return [][]byte{payload}, err
	}
	id := t.attempt.hooks.deliveredResponseID
	var result [][]byte
	if id == "" {
		id = "mekugi_wait_" + rand.Text()
		result = append(result, responses.MustMarshalJSON(map[string]any{"type": "response.created", "response": map[string]any{"id": id, "status": "in_progress", "output": []any{}}}))
	}
	return append(result, responses.MustMarshalJSON(map[string]any{"type": "response.completed", "response": map[string]any{"id": id, "status": "completed", "output": []any{}}})), nil
}

func (usageLimitResponse) Finish(bool) error { return nil }
