package requestexecution

import (
	"bytes"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"strings"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	"github.com/yusing/mekugi/modules/agentguidance"
)

// compactSummary observes the ordinary provider turn. It retains the model's
// assistant answer, not reasoning items or a second representation of history.
type compactSummary struct {
	*ResponseHooks
	response map[string]jsontext.Value
	assistantTextCollector
	failure []byte
	err     error
	output  io.Writer
	created []byte
	started bool
}

func (c *compactSummary) Observe(payload []byte, stream bool) {
	c.ResponseHooks.Observe(payload, stream)
	if c.err != nil || len(payload) == 0 {
		return
	}
	if len(payload) > codexresponses.MaxResponseBytes {
		c.err = errors.New("compaction provider response exceeds capacity")
		return
	}
	var fields map[string]jsontext.Value
	if c.err = json.Unmarshal(payload, &fields); c.err != nil {
		return
	}
	if stream {
		switch responses.StringField(fields, "type") {
		case "response.created":
			var response map[string]jsontext.Value
			if c.err = json.Unmarshal(fields["response"], &response); c.err == nil {
				c.created = responses.MustMarshalJSON(map[string]any{"type": "response.created", "response": map[string]any{"id": responses.StringField(response, "id"), "status": "in_progress", "output": []any{}}})
			}
			return
		case "response.output_item.done":
			_, c.err = c.assistantTextCollector.TransformJSON(responses.MustMarshalJSON(map[string]any{"output": []jsontext.Value{fields["item"]}}))
			return
		case "response.completed", "response.failed", "response.incomplete":
			payload = fields["response"]
			fields = nil
			if c.err = json.Unmarshal(payload, &fields); c.err != nil {
				return
			}
		case "error":
			c.failure = append([]byte(nil), payload...)
			return
		default:
			return
		}
	}
	c.response = fields
	_, c.err = c.assistantTextCollector.TransformJSON(payload)
}

// Collecting provider output does not confirm delivery to the host.
func (*compactSummary) Delivered([]byte) {}

// CopyBody writes whole SSE frames and their separators. Project every upstream
// frame, including comments, into a data event: Codex's SSE idle timer ignores
// comments, and its WebSocket transport has no SSE comments at all. Never expose
// provider summary/reload content before retained publication.
func (c *compactSummary) Write(frame []byte) (int, error) {
	if len(bytes.TrimSpace(frame)) == 0 {
		return len(frame), nil
	}
	payload := []byte(`{"type":"response.in_progress"}`)
	if !c.started && len(c.created) != 0 {
		payload = c.created
	}
	if err := codexresponses.WriteEvent(c.output, payload); err != nil {
		return 0, err
	}
	if len(c.created) != 0 {
		c.started = true
	}
	c.ResponseHooks.Delivered(payload)
	return len(frame), nil
}

func (a *requestAttempt) deliverCompactSummary() error {
	a.finalization.failurePhase = requestFailureInspectResponse
	collected := &compactSummary{ResponseHooks: a.hooks, output: a.executor.Output}
	defer a.response.Body.Close()
	var reader io.Reader = a.response.Body
	var collector io.Writer = io.Discard
	if a.streamResponse {
		collector = collected
		if writer, ok := a.executor.Output.(http.ResponseWriter); ok {
			writer.Header().Set("Content-Type", "text/event-stream")
			if err := http.NewResponseController(writer).Flush(); err != nil {
				a.finalization.failurePhase = requestFailureWriteResponse
				return err
			}
		}
		if diagnostics := a.hooks.StreamDiagnostics; diagnostics != nil {
			diagnostics.ResponseStarted(a.response.Response, a.hooks.Transport)
			if id := a.response.Header.Get("X-Request-Id"); responses.SafeDiagnosticIdentity(id) {
				diagnostics.ProviderRequestID = id
			}
		}
		reader = diagnosticStreamReader{reader: reader, diagnostics: a.hooks.StreamDiagnostics, receivedAt: &a.hooks.ReceivedAt}
	}
	state, err := codexresponses.CopyBody(collector, reader, a.streamResponse, nil, collected)
	if a.streamResponse {
		recordStreamCopyStop(a.hooks.StreamDiagnostics, err)
	}
	a.finalization.upstreamTerminalState = state
	if err != nil {
		a.finalization.classifyCopyError(err)
		return err
	}
	var producerErr error
	if state == responses.TerminalFailed {
		if body, ok := a.response.Body.(interface{ TerminalError() error }); ok {
			producerErr = body.TerminalError()
		}
	}
	a.finalization.failurePhase = requestFailureTransform
	if collected.err != nil {
		return collected.err
	}
	if state != responses.TerminalCompleted {
		if status := responses.StringField(collected.response, "status"); status == "failed" || status == "incomplete" {
			collected.response["output"] = jsontext.Value(`[]`)
			collected.failure = responses.MustMarshalJSON(map[string]any{"type": "response." + status, "response": collected.response})
		}
		if len(collected.failure) == 0 {
			return ErrMissingTerminal
		}
		if writer, ok := a.executor.Output.(http.ResponseWriter); ok {
			writer.Header().Set("Content-Type", "text/event-stream")
		}
		a.finalization.failurePhase = requestFailureWriteResponse
		if err := codexresponses.WriteEvent(a.executor.Output, collected.failure); err != nil {
			return err
		}
		a.hooks.Delivered(collected.failure)
		if producerErr != nil {
			a.finalization.classifyCopyError(producerErr)
			return producerErr
		}
		a.finalization.outcome = requestOutcomeFailed
		a.finalization.failurePhase = requestFailureTerminalValidation
		return nil
	}

	if responses.StringField(collected.response, "status") != "completed" {
		return errors.New("invalid compaction provider completion")
	}
	summary, metadata, selected := agentguidance.SplitCompactReload(collected.text)
	if strings.TrimSpace(summary) == "" {
		return errors.New("provider returned an empty compaction summary")
	}
	id := responses.StringField(collected.response, "id")
	if id == "" {
		return errors.New("compaction summary has no provider response identity")
	}
	if !selected {
		metadata = ""
	}
	if err := a.executionCtx.Err(); err != nil {
		return err
	}
	if a.executor.CompactStore == nil {
		return errors.New("compaction summary storage is unavailable")
	}
	reference, err := a.executor.CompactStore(a.executionCtx, a.threadID, summary, metadata)
	if err != nil {
		return err
	}
	message := map[string]any{"type": "compaction", "encrypted_content": reference}
	collected.response["output"] = responses.MustMarshalJSON([]any{message})
	if writer, ok := a.executor.Output.(http.ResponseWriter); ok {
		writer.Header().Set("Content-Type", "text/event-stream")
	}
	a.finalization.failurePhase = requestFailureWriteResponse
	if !collected.started {
		payload := responses.MustMarshalJSON(map[string]any{"type": "response.created", "response": map[string]any{"id": id, "status": "in_progress", "output": []any{}}})
		if err := codexresponses.WriteEvent(a.executor.Output, payload); err != nil {
			return err
		}
		a.hooks.Delivered(payload)
	}
	for _, event := range []any{
		map[string]any{"type": "response.output_item.added", "output_index": 0, "item": message},
		map[string]any{"type": "response.output_item.done", "output_index": 0, "item": message},
		map[string]any{"type": "response.completed", "response": collected.response},
	} {
		payload := responses.MustMarshalJSON(event)
		if err := codexresponses.WriteEvent(a.executor.Output, payload); err != nil {
			return err
		}
		a.hooks.Delivered(payload)
	}
	a.finalization.outcome = requestOutcomeCompleted
	a.finalization.failurePhase = ""
	return nil
}
