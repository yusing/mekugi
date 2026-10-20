// Source: internal/router/stream_diagnostics.go@b5162407, bounded metadata.
package capture

import (
	"cmp"
	"context"
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

// Stream retains bounded metadata only. It neither assembles tool
// input nor participates in translation, retry, or transport ownership.
type Stream struct {
	Transport               string    `json:"transport,omitempty"`
	HTTPFraming             string    `json:"http_framing,omitempty"`
	BodyDecoded             bool      `json:"body_decoded,omitempty"`
	HTTPContentLength       *int64    `json:"http_content_length,omitzero"`
	ProviderErrorCode       string    `json:"provider_error_code,omitempty"`
	ProviderResponseID      string    `json:"provider_response_id,omitempty"`
	BodyBytes               uint64    `json:"body_bytes"`
	Events                  uint64    `json:"events"`
	LastByteAt              time.Time `json:"last_byte_at,omitzero"`
	ReadEndedAt             time.Time `json:"read_ended_at,omitzero"`
	EndReason               string    `json:"end_reason,omitempty"`
	UnterminatedEvent       bool      `json:"unterminated_event,omitempty"`
	WriteTermination        string    `json:"write_termination,omitempty"`
	WriteWebSocketCloseCode int       `json:"write_websocket_close_code,omitempty"`
	CopyStop                string    `json:"copy_stop,omitempty"`
	LastEvent               string    `json:"last_event,omitempty"`
	LastEventAt             time.Time `json:"last_event_at,omitzero"`
	TerminalEvent           string    `json:"terminal_event,omitempty"`
	ReadOrigin              string    `json:"read_origin,omitempty"`
	ReadTermination         string    `json:"read_termination,omitempty"`
	WebSocketCloseCode      int       `json:"websocket_close_code,omitempty"`
	ProviderRequestID       string    `json:"provider_request_id,omitempty"`
	Truncated               bool      `json:"pending_calls_truncated,omitempty"`
	pending                 map[string]*StreamCall
}

type StreamCall struct {
	ItemID     string `json:"item_id"`
	CallID     string `json:"call_id,omitempty"`
	Fragments  uint64 `json:"fragments"`
	InputBytes uint64 `json:"input_bytes"`
	InputDone  bool   `json:"input_done"`
}

func (d *Stream) ResponseStarted(response *http.Response, fallback string) {
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
		d.Transport = fallback
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

const maxStreamDiagnosticCalls = 32

func (d *Stream) Observe(payload []byte) {
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
	if json.Unmarshal(payload, &event, jsonv1.DefaultOptionsV1()) != nil {
		d.LastEvent = "invalid_json"
		if string(payload) == "[DONE]" {
			d.LastEvent = "done_marker"
		}
		return
	}
	if (event.Type == responses.Created || event.Type == responses.InProgress || event.Type.Terminal()) && responses.SafeDiagnosticIdentity(event.Response.ID) {
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
		if json.Unmarshal(payload, &failure, jsonv1.DefaultOptionsV1()) == nil {
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
			Headers map[string]jsonv1.RawMessage `json:"headers"`
		}
		if json.Unmarshal(payload, &metadata, jsonv1.DefaultOptionsV1()) == nil {
			for key, raw := range metadata.Headers {
				if http.CanonicalHeaderKey(key) == "X-Request-Id" {
					var id string
					if json.Unmarshal(raw, &id, jsonv1.DefaultOptionsV1()) == nil && responses.SafeDiagnosticIdentity(id) {
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
		if !responses.SafeDiagnosticIdentity(event.Item.ID) {
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
			d.pending = make(map[string]*StreamCall)
		}
		call := &StreamCall{ItemID: event.Item.ID}
		if responses.SafeDiagnosticIdentity(event.Item.CallID) {
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

// NewStream attaches bounded metadata only to a recorded logical request.
func NewStream(ctx context.Context) *Stream {
	if _, ok := ctx.Value(captureKey{}).(*requestState); !ok {
		return nil
	}
	return new(Stream)
}

func (d *Stream) ReadBytes(n int, at time.Time) {
	if d == nil || n == 0 {
		return
	}
	d.BodyBytes += uint64(n)
	d.LastByteAt = at.UTC()
}

func (d *Stream) ReadEnded(at time.Time, code, origin string, closeCode int) {
	if d == nil || d.ReadTermination != "" {
		return
	}
	d.ReadEndedAt = at.UTC()
	d.ReadOrigin = cmp.Or(origin, d.ReadOrigin, "upstream")
	d.ReadTermination = code
	if d.ReadOrigin != "upstream" {
		d.ReadTermination = d.ReadOrigin + "_" + strings.TrimPrefix(code, "upstream_")
	}
	if closeCode != -1 {
		d.WebSocketCloseCode = closeCode
	}
}

func (d *Stream) WriteStopped(code string, closeCode int) {
	if d == nil {
		return
	}
	d.WriteTermination = "downstream_" + strings.TrimPrefix(code, "upstream_")
	if closeCode != -1 {
		d.WriteWebSocketCloseCode = closeCode
	}
}

func (d *Stream) CopyStopped(category string) {
	if d == nil {
		return
	}
	d.CopyStop = category
	if category == "" {
		if d.TerminalEvent != "" {
			d.CopyStop = "terminal_event"
		} else {
			d.CopyStop = "eof_without_terminal"
		}
	}
	d.classifyEnd()
}

func (d *Stream) classifyEnd() {
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

type StreamSnapshot struct {
	Stream
	PendingCalls []StreamCall `json:"pending_calls"`
}

func (d *Stream) Snapshot() *StreamSnapshot {
	if d == nil {
		return nil
	}
	result := &StreamSnapshot{Stream: *d, PendingCalls: make([]StreamCall, 0, len(d.pending))}
	result.pending = nil
	for _, call := range d.pending {
		result.PendingCalls = append(result.PendingCalls, *call)
	}
	slices.SortFunc(result.PendingCalls, func(a, b StreamCall) int { return cmp.Compare(a.ItemID, b.ItemID) })
	return result
}
