package router

import (
	"cmp"
	"encoding/json"
	"errors"
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

const maxStreamDiagnosticCalls = 32

func (d *streamDiagnostics) observe(payload []byte) {
	if d == nil {
		return
	}
	d.LastEventAt = time.Now().UTC()
	var event struct {
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
	if d == nil || err == nil {
		return
	}
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
	if d == nil {
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
