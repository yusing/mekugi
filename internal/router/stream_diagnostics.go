package router

import (
	"cmp"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/yusing/mekugi/internal/responses"
)

// streamDiagnostics retains bounded metadata only. It neither assembles tool
// input nor participates in translation, retry, or transport ownership.
type streamDiagnostics struct {
	Transport          string    `json:"transport,omitempty"`
	HTTPFraming        string    `json:"http_framing,omitempty"`
	BodyDecoded        bool      `json:"body_decoded,omitempty"`
	HTTPContentLength  *int64    `json:"http_content_length,omitzero"`
	ProviderErrorCode  string    `json:"provider_error_code,omitempty"`
	ProviderResponseID string    `json:"provider_response_id,omitempty"`
	BodyBytes          uint64    `json:"body_bytes"`
	Events             uint64    `json:"events"`
	LastByteAt         time.Time `json:"last_byte_at,omitzero"`
	ReadEndedAt        time.Time `json:"read_ended_at,omitzero"`
	EndReason          string    `json:"end_reason,omitempty"`
	UnterminatedEvent  bool      `json:"unterminated_event,omitempty"`
	CopyStop           string    `json:"copy_stop,omitempty"`
	LastEvent          string    `json:"last_event,omitempty"`
	LastEventAt        time.Time `json:"last_event_at,omitzero"`
	TerminalEvent      string    `json:"terminal_event,omitempty"`
	ReadOrigin         string    `json:"read_origin,omitempty"`
	ReadTermination    string    `json:"read_termination,omitempty"`
	WebSocketCloseCode int       `json:"websocket_close_code,omitempty"`
	ProviderRequestID  string    `json:"provider_request_id,omitempty"`
	Truncated          bool      `json:"pending_calls_truncated,omitempty"`
	pending            map[string]*streamCallDiagnostic
}

type streamCallDiagnostic struct {
	ItemID     string `json:"item_id"`
	CallID     string `json:"call_id,omitempty"`
	Fragments  uint64 `json:"fragments"`
	InputBytes uint64 `json:"input_bytes"`
	InputDone  bool   `json:"input_done"`
}

func (d *streamDiagnostics) responseStarted(response *http.Response) {
	if d == nil {
		return
	}
	d.BodyDecoded = response.Uncompressed
	switch response.ProtoMajor {
	case 1:
		d.Transport = "http1"
	case 2:
		d.Transport = "http2"
	case 3:
		d.Transport = "http3"
	default:
		switch response.Body.(type) {
		case *webSocketResponseBody, *webSocketExchange:
			d.Transport = "websocket"
		case *grokResponseBody:
			d.Transport = "chat_response_bridge"
		default:
			d.Transport = "unknown"
		}
		return
	}
	switch {
	case slices.Contains(response.TransferEncoding, "chunked"):
		d.HTTPFraming = "chunked"
	case response.ContentLength >= 0:
		d.HTTPFraming = "content_length"
		d.HTTPContentLength = new(response.ContentLength)
	case response.ProtoMajor >= 2:
		d.HTTPFraming = "stream_end"
	default:
		d.HTTPFraming = "connection_close"
	}
}

// Observe decoded/adapted body delivery, not wire bytes, without buffering content.
type diagnosticStreamReader struct {
	reader      io.Reader
	diagnostics *streamDiagnostics
}

func (r diagnosticStreamReader) Read(buffer []byte) (int, error) {
	n, err := r.reader.Read(buffer)
	if n > 0 {
		r.diagnostics.BodyBytes += uint64(n)
		r.diagnostics.LastByteAt = time.Now().UTC()
	}
	r.diagnostics.readEnded(err)
	return n, err
}

const maxStreamDiagnosticCalls = 32

func (d *streamDiagnostics) observe(payload []byte) {
	if d == nil {
		return
	}
	d.Events++
	d.LastEventAt = time.Now().UTC()
	var event struct {
		Response struct {
			ID string `json:"id"`
		} `json:"response"`
		Type   responses.Kind `json:"type"`
		ItemID string         `json:"item_id"`
		Delta  string         `json:"delta"`
		Item   struct {
			Status string `json:"status"`
			Type   string `json:"type"`
			ID     string `json:"id"`
			CallID string `json:"call_id"`
		} `json:"item"`
	}
	if json.Unmarshal(payload, &event) != nil {
		d.LastEvent = "invalid_json"
		if string(payload) == "[DONE]" {
			d.LastEvent = "done_marker"
		}
		return
	}
	if (event.Type == responses.Created || event.Type == responses.InProgress || event.Type.Terminal()) && safeFeatureIdentity(event.Response.ID) {
		d.ProviderResponseID = event.Response.ID
	}
	switch event.Type {
	case responses.Created, responses.Completed, responses.Failed, responses.Incomplete,
		responses.InProgress, responses.OutputItemAdded, responses.OutputItemDone,
		responses.FunctionArgumentsDelta, responses.FunctionArgumentsDone,
		responses.CustomInputDelta, responses.CustomInputDone, responses.OutputTextDelta,
		responses.OutputTextDone, responses.ContentPartAdded, responses.ContentPartDone,
		responses.Error, responses.Metadata, responses.RateLimits, responses.WebSocketTiming:
		d.LastEvent = string(event.Type)
	default:
		d.LastEvent = "other"
	}
	if event.Type.EndsExchange() {
		d.TerminalEvent = d.LastEvent
	}
	if event.Type == responses.Error || event.Type == responses.Failed {
		var failure struct {
			Code  string `json:"code"`
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
			Response struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			} `json:"response"`
		}
		if json.Unmarshal(payload, &failure) == nil {
			code := cmp.Or(failure.Error.Code, failure.Response.Error.Code, failure.Code)
			// Only known protocol codes are safe; arbitrary strings can echo
			// request content even when they look like identifiers.
			switch code {
			case "rate_limit_exceeded", "insufficient_quota", "invalid_api_key",
				"invalid_request_error", "context_length_exceeded", "server_error",
				"internal_server_error", "model_not_found", "invalid_encrypted_content",
				"previous_response_not_found":
				d.ProviderErrorCode = code
			default:
				d.ProviderErrorCode = "other"
			}
		}
	}
	if event.Type == responses.Metadata || event.Type == responses.Error {
		var metadata struct {
			Headers map[string]json.RawMessage `json:"headers"`
		}
		if json.Unmarshal(payload, &metadata) == nil {
			for key, raw := range metadata.Headers {
				if http.CanonicalHeaderKey(key) == "X-Request-Id" {
					var id string
					if json.Unmarshal(raw, &id) == nil && safeFeatureIdentity(id) {
						d.ProviderRequestID = id
					}
				}
			}
		}
	}
	switch event.Type {
	case responses.OutputItemAdded:
		if event.Item.Type != "custom_tool_call" && event.Item.Type != "function_call" {
			return
		}
		if !safeFeatureIdentity(event.Item.ID) {
			d.Truncated = true
			return
		}
		if _, exists := d.pending[event.Item.ID]; exists {
			return
		}
		if len(d.pending) >= maxStreamDiagnosticCalls {
			d.Truncated = true
			return
		}
		if d.pending == nil {
			d.pending = make(map[string]*streamCallDiagnostic)
		}
		call := &streamCallDiagnostic{ItemID: event.Item.ID}
		if safeFeatureIdentity(event.Item.CallID) {
			call.CallID = event.Item.CallID
		}
		d.pending[event.Item.ID] = call
	case responses.CustomInputDelta, responses.FunctionArgumentsDelta:
		if call := d.pending[event.ItemID]; call != nil {
			call.Fragments++
			call.InputBytes += uint64(len(event.Delta))
		}
	case responses.CustomInputDone, responses.FunctionArgumentsDone:
		if call := d.pending[event.ItemID]; call != nil {
			call.InputDone = true
		}
	case responses.OutputItemDone:
		if event.Item.Status == "" || event.Item.Status == "completed" {
			delete(d.pending, event.Item.ID)
		}
	}
}

func (d *streamDiagnostics) readEnded(err error) {
	if d == nil || err == nil || d.ReadTermination != "" {
		return
	}
	d.ReadEndedAt = time.Now().UTC()
	if d.ReadOrigin == "" {
		d.ReadOrigin = "upstream"
	}
	diagnostic, _ := errors.AsType[*criticalDiagnosticError](forwardCriticalDiagnostic(err))
	d.ReadTermination = diagnostic.code
	if errors.Is(err, errUpstreamStreamIdleTimeout) {
		d.ReadTermination = "upstream_idle_timeout"
	}
	if d.ReadOrigin != "upstream" {
		d.ReadTermination = d.ReadOrigin + "_" + strings.TrimPrefix(d.ReadTermination, "upstream_")
	}
	if code := websocket.CloseStatus(err); code != -1 {
		d.WebSocketCloseCode = int(code)
	}
}

func (d *streamDiagnostics) readEndedFrom(err error, origin string) {
	if d == nil || d.ReadTermination != "" {
		return
	}
	d.ReadOrigin = origin
	d.readEnded(err)
}

func (d *streamDiagnostics) copyStopped(err error) {
	if d == nil {
		return
	}
	switch {
	case errors.Is(err, errResponseWrite):
		d.CopyStop = "downstream_write_error"
	case errors.Is(err, errResponseTransform):
		d.CopyStop = "translation_error"
	case err != nil:
		d.CopyStop = "read_error"
	case d.TerminalEvent != "":
		d.CopyStop = "terminal_event"
	default:
		d.CopyStop = "eof_without_terminal"
	}
	d.classifyEnd()
}

func (d *streamDiagnostics) classifyEnd() {
	d.EndReason = d.ReadTermination
	if d.TerminalEvent != "" || d.ReadTermination != "upstream_eof" {
		return
	}
	if d.BodyDecoded {
		d.EndReason = "http_decoded_body_ended_without_terminal"
		return
	}
	switch d.HTTPFraming {
	case "content_length", "chunked", "stream_end":
		d.EndReason = "http_body_complete_without_terminal"
	case "connection_close":
		d.EndReason = "http_connection_closed_without_terminal"
	default:
		if d.Transport == "websocket" {
			d.EndReason = "websocket_eof_without_terminal"
		} else {
			d.EndReason = "stream_eof_without_terminal"
		}
	}
}

func (d *streamDiagnostics) snapshot() any {
	calls := make([]streamCallDiagnostic, 0, len(d.pending))
	for _, call := range d.pending {
		calls = append(calls, *call)
	}
	slices.SortFunc(calls, func(a, b streamCallDiagnostic) int { return cmp.Compare(a.ItemID, b.ItemID) })
	return struct {
		*streamDiagnostics
		PendingCalls []streamCallDiagnostic `json:"pending_calls"`
	}{d, calls}
}
