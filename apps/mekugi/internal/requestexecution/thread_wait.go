package requestexecution

import (
	"crypto/sha256"
	json "encoding/json/v2"
	"fmt"
	"net/http"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

// completeLocalWait closes a successfully registered logical wait without
// forwarding another inference, observing provider usage or synthesizing input.
func (a *requestAttempt) completeLocalWait() (bool, error) {
	if a.executor.ThreadControl == nil || !a.metadataValid || a.metadata.RequestKind != responses.Turn || a.prewarm {
		return false, nil
	}
	prepared, ok := a.prepared.(interface{ LocalWaitItem() string })
	if !ok || prepared.LocalWaitItem() == "" {
		return false, nil
	}
	stop, err := a.executor.ThreadControl.ClaimLocalStop(a.executionCtx, a.threadID, a.metadata.TurnID, prepared.LocalWaitItem())
	if err != nil || stop == nil {
		return false, err
	}
	if a.prepared != nil {
		if err := a.prepared.Finish(false); err != nil {
			return true, err
		}
	}
	id := fmt.Sprintf("mekugi_wait_%x", sha256.Sum256([]byte(a.threadID+"\x00"+stop.Turn+"\x00"+stop.Wait)))
	response := map[string]any{"id": id, "object": "response", "status": "completed", "output": []any{}}
	stream := a.request.Stream || a.executor.Exchange != nil
	if writer, ok := a.executor.Output.(http.ResponseWriter); ok {
		contentType := "application/json"
		if stream {
			contentType = "text/event-stream"
		}
		writer.Header().Set("Content-Type", contentType)
		writer.WriteHeader(http.StatusOK)
	}
	a.finalization.failurePhase = requestFailureWriteResponse
	if stream {
		created := map[string]any{"id": id, "object": "response", "status": "in_progress", "output": []any{}}
		for _, event := range []map[string]any{{"type": "response.created", "response": created}, {"type": "response.completed", "response": response}} {
			data, err := json.Marshal(event)
			if err != nil {
				return true, err
			}
			if err := codexresponses.WriteEvent(a.executor.Output, data); err != nil {
				return true, err
			}
			a.hooks.Delivered(data)
		}
	} else {
		data, err := json.Marshal(response)
		if err != nil {
			return true, err
		}
		if _, err := a.executor.Output.Write(data); err != nil {
			return true, err
		}
	}
	a.finalization.failurePhase = ""
	a.finalization.observation.outcome = requestOutcomeCompleted
	return true, nil
}
