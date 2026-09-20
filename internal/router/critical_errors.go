package router

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"sync"
	"syscall"

	"github.com/coder/websocket"
	responseevents "github.com/yusing/mekugi/internal/responses"
)

// CriticalErrors retains bounded, actionable session notices, including sanitized
// provider error details, but no request snapshots or operational event history. It outlives router shutdown so
// the launcher can report notices that could not reach Codex.
type CriticalErrors struct {
	mu             sync.Mutex
	entries        []*criticalNotice
	overflow       uint64
	diagnosticSalt string
}

type criticalNotice struct {
	session, category, message, id string
	count, delivered               uint64
	inFlight                       bool
}

func NewCriticalErrors() *CriticalErrors { return &CriticalErrors{diagnosticSalt: rand.Text()} }

// criticalDiagnosticError separates producer-owned diagnostic summaries from
// sanitized caller-facing provider details. Arbitrary wrapped error text is not
// copied into notices or sanitized diagnostics.
type criticalDiagnosticError struct {
	err          error
	code         string
	summary      string
	callerDetail string
	distinct     bool
}

func (e *criticalDiagnosticError) Error() string {
	if e.callerDetail != "" {
		return e.callerDetail
	}
	return e.err.Error()
}
func (e *criticalDiagnosticError) Unwrap() error { return e.err }

func criticalDiagnostic(err error, code, summary string, distinct bool) error {
	if err == nil {
		return nil
	}
	if _, ok := errors.AsType[*criticalDiagnosticError](err); ok {
		return err
	}
	return &criticalDiagnosticError{err: err, code: code, summary: summary, distinct: distinct}
}

// Classify wrapped transport errors without copying addresses, close reasons,
// URLs, headers, or arbitrary error text into notices or debug records.
func forwardCriticalDiagnostic(err error) error {
	if _, ok := errors.AsType[*requestCompatibilityError](err); ok {
		return err
	}
	code, summary := "upstream_transport_unknown", "the upstream transport failed without a recognized error type"
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		code, summary = "upstream_deadline", "the upstream request exceeded its deadline"
	case errors.Is(err, context.Canceled):
		code, summary = "upstream_canceled", "the upstream request was canceled"
	case websocket.CloseStatus(err) != -1:
		code = fmt.Sprintf("upstream_websocket_close_%d", websocket.CloseStatus(err))
		summary = fmt.Sprintf("the upstream WebSocket closed with status %d", websocket.CloseStatus(err))
	case errors.Is(err, io.ErrUnexpectedEOF):
		code, summary = "upstream_unexpected_eof", "the upstream connection ended unexpectedly"
	case errors.Is(err, io.EOF):
		code, summary = "upstream_eof", "the upstream connection closed before a response was available"
	case errors.Is(err, syscall.ECONNRESET):
		code, summary = "upstream_connection_reset", "the upstream connection was reset"
	case errors.Is(err, syscall.ECONNREFUSED):
		code, summary = "upstream_connection_refused", "the upstream connection was refused"
	case errors.Is(err, syscall.EPIPE):
		code, summary = "upstream_broken_pipe", "the upstream connection closed while sending the request"
	default:
		if _, ok := errors.AsType[*net.DNSError](err); ok {
			code, summary = "upstream_dns", "the upstream hostname could not be resolved"
		} else if network, ok := errors.AsType[net.Error](err); ok && network.Timeout() {
			code, summary = "upstream_network_timeout", "the upstream network operation timed out"
		} else if operation, ok := errors.AsType[*net.OpError](err); ok {
			switch operation.Op {
			case "dial", "read", "write":
				code = "upstream_network_" + operation.Op
				summary = "the upstream network " + operation.Op + " operation failed"
			}
		}
	}
	if rejection, ok := errors.AsType[*webSocketStatusError](err); ok {
		code = fmt.Sprintf("upstream_websocket_http_%d", rejection.status)
		summary = fmt.Sprintf("the upstream WebSocket handshake was rejected with HTTP %d", rejection.status)
	}
	if rejection, ok := errors.AsType[*providerHTTPError](err); ok {
		code = fmt.Sprintf("upstream_provider_http_%d", rejection.status)
		summary = fmt.Sprintf("the provider rejected the request with HTTP %d", rejection.status)
	}
	return criticalDiagnostic(err, code, summary, true)
}

func staticCriticalDiagnostic(code, summary string) error {
	return &criticalDiagnosticError{err: errors.New(summary), code: code, summary: summary}
}

func (c *CriticalErrors) diagnosticReference(f *requestFinalization, err error) string {
	mac := hmac.New(sha256.New, []byte(c.diagnosticSalt))
	_, _ = fmt.Fprintf(mac, "%s|%d|%s|", f.failurePhase, f.upstreamStatusCode, f.upstreamTerminalState)
	if err == nil {
		_, _ = mac.Write([]byte("request failed without a wrapped error"))
	} else {
		_, _ = mac.Write([]byte(err.Error()))
	}
	return fmt.Sprintf("%x", mac.Sum(nil)[:6])
}

func (c *CriticalErrors) record(f *requestFinalization, err error) {
	if c == nil || f.observation.outcome == requestOutcomeCompleted ||
		f.observation.outcome == requestOutcomeCanceledBeforeResponse || f.observation.outcome == requestOutcomeCanceledAfterResponse {
		return
	}
	if err == nil && f.providerFailure != nil {
		err = f.providerFailure
	}
	f.diagnosticReference = c.diagnosticReference(f, err)
	f.diagnosticCode = "unclassified"
	if diagnostic, ok := errors.AsType[*criticalDiagnosticError](err); ok {
		f.diagnosticCode = diagnostic.code
	}
	if compatibility, ok := errors.AsType[*requestCompatibilityError](err); ok {
		f.diagnosticCode = compatibility.code
	}
	category := string(f.failurePhase)
	message := "Mekugi could not complete the request. Retry the turn; if it persists, restart the session."
	if diagnostic, ok := errors.AsType[*criticalDiagnosticError](err); ok && diagnostic.callerDetail != "" {
		category, message = diagnostic.code+":"+f.diagnosticReference, diagnostic.callerDetail
	} else if compatibility, ok := errors.AsType[*requestCompatibilityError](err); ok {
		category, message = compatibility.code, compatibility.Error()
	} else if rejection, ok := errors.AsType[*providerHTTPError](err); ok {
		category, message = "provider_http_error:"+f.diagnosticReference, rejection.Error()
	} else {
		switch {
		case f.upstreamStatusCode == 401 || f.upstreamStatusCode == 403:
			category, message = "authentication", "Mekugi upstream authentication was rejected. Check your Codex or Grok credentials before retrying."
		case f.upstreamStatusCode == 429:
			category, message = "rate_limit", "The upstream service rate-limited this turn. Wait before retrying."
		case errors.Is(err, context.DeadlineExceeded), errors.Is(err, errUpstreamStreamIdleTimeout):
			category, message = "timeout", "The upstream response timed out. Retry the turn."
		case f.failurePhase == requestFailurePrepare:
			message = "Mekugi could not prepare this request. Check the session's tool and configuration compatibility before retrying."
		case f.failurePhase == requestFailureTransform:
			message = "Mekugi could not safely translate the response. No unsupported tool call was released."
		}
		if category == string(f.failurePhase) {
			reference := f.diagnosticReference
			phase := requestFailureDescription(f.failurePhase)
			diagnostic, _ := errors.AsType[*criticalDiagnosticError](err)
			switch {
			case diagnostic != nil:
			case errors.Is(err, errUpstreamResponseWithoutTerminal):
				state := f.upstreamTerminalState.String()
				diagnostic = &criticalDiagnosticError{code: "missing_upstream_terminal:" + state, summary: "the upstream response ended without a completed or failed terminal state; observed state was " + state}
			case errors.Is(err, errResponseWrite), f.failurePhase == requestFailureWriteResponse:
				diagnostic = &criticalDiagnosticError{code: "downstream_response_write", summary: "the downstream response could not be written", distinct: true}
			case f.failurePhase == requestFailureTerminalValidation && err == nil && f.upstreamTerminalState != responseTerminalUnknown:
				diagnostic = &criticalDiagnosticError{code: "upstream_" + f.upstreamTerminalState.String(), summary: "the upstream response reported terminal state " + f.upstreamTerminalState.String()}
			}
			message += " Failure phase: " + phase + "."
			if diagnostic != nil {
				f.diagnosticCode = diagnostic.code
				category += ":" + diagnostic.code
				if diagnostic.distinct {
					category += ":" + reference
				}
				message += " Cause: " + diagnostic.summary + ". Diagnostic reference: " + reference + "."
			} else {
				// Unknown errors may contain unquoted prompts, scripts, headers, or
				// credentials. Retain a correlation reference and phase without
				// copying arbitrary error text into a user-visible notice.
				category += ":unclassified:" + reference
				message += " The detailed cause was not safe for display. Diagnostic reference: " + reference + "."
			}
		} else {
			if f.diagnosticCode == "unclassified" {
				f.diagnosticCode = category
			}
			category += ":" + f.diagnosticReference
			message += " Diagnostic reference: " + f.diagnosticReference + "."
		}
	}
	// Observe this request's safe description before session deduplication. A
	// routing session can be shared or remapped; its retained queue cannot tell
	// us which thread produced an earlier failure.
	noticeID := commentaryMessageID("critical:" + f.sessionID + ":" + category)
	if f.observeCriticalNotice != nil {
		f.observeCriticalNotice(noticeID, message)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, notice := range c.entries {
		if notice.session == f.sessionID && notice.category == category {
			notice.count++
			return
		}
	}
	if len(c.entries) >= 256 {
		c.overflow++
		return
	}
	c.entries = append(c.entries, &criticalNotice{session: f.sessionID, category: category, message: message,
		id: noticeID, count: 1})
}

// Auxiliary degradation and automatic cleanup are user-visible without turning
// successful work into a failed request. Messages here are producer-owned text,
// not arbitrary request data. Empty session denotes a router-wide notice.
func (c *CriticalErrors) addNotice(session, category, message string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, notice := range c.entries {
		if notice.session == session && notice.category == category {
			notice.count++
			return
		}
	}
	if len(c.entries) >= 256 {
		c.overflow++
		return
	}
	c.entries = append(c.entries, &criticalNotice{
		session: session, category: category, message: message,
		id: commentaryMessageID("notice:" + session + ":" + category), count: 1,
	})
}

func requestFailureDescription(phase requestFailurePhase) string {
	switch phase {
	case requestFailurePrepare:
		return "request preparation"
	case requestFailureForward:
		return "upstream request forwarding"
	case requestFailureInspectResponse:
		return "upstream response inspection"
	case requestFailureStreamIdleTimeout:
		return "upstream stream waiting"
	case requestFailureTransform:
		return "response translation"
	case requestFailureWriteResponse:
		return "downstream response writing"
	case requestFailureTerminalValidation:
		return "terminal response validation"
	default:
		return "request processing"
	}
}

func noticeText(n *criticalNotice) string {
	if n.count > 1 {
		return fmt.Sprintf("%s (occurred %d times)", n.message, n.count)
	}
	return n.message
}

// Pending is called after the router has stopped, outside Codex's terminal UI.
func (c *CriticalErrors) Pending() []string {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var result []string
	for _, n := range c.entries {
		if n.count > n.delivered {
			result = append(result, "mekugi: "+noticeText(n))
		}
	}
	if c.overflow != 0 {
		result = append(result, fmt.Sprintf("mekugi: %d additional failures could not be retained; check the failed turns.", c.overflow))
	}
	return result
}

// stripInput removes exact router-owned IDs for this session, including notices
// replayed after a client disconnected before delivery could be confirmed.
func (c *CriticalErrors) stripInput(request *parsedResponsesRequest, session string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var items []map[string]json.RawMessage
	if json.Unmarshal(request.fields["input"], &items) != nil {
		return
	}
	before := len(items)
	items = slices.DeleteFunc(items, func(item map[string]json.RawMessage) bool {
		if jsonString(item, "type") != "message" {
			return false
		}
		for _, n := range c.entries {
			if (n.session == session || n.session == "") && n.id == jsonString(item, "id") {
				return true
			}
		}
		return false
	})
	if len(items) != before {
		request.fields["input"] = mustMarshalJSON(items)
	}
}

type criticalErrorTransform struct {
	owner    *CriticalErrors
	notices  []*criticalNotice
	counts   []uint64
	messages []map[string]json.RawMessage
	subagent bool
	emitted  bool
}

func (c *CriticalErrors) transform(session string, subagent bool) *criticalErrorTransform {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &criticalErrorTransform{owner: c, subagent: subagent}
	for _, n := range c.entries {
		// Repeats after the first visible notice are summarized only at shutdown.
		if n.session != session && (n.session != "" || subagent) || n.delivered != 0 || n.inFlight {
			continue
		}
		n.inFlight = true
		t.notices = append(t.notices, n)
		t.counts = append(t.counts, n.count)
		t.messages = append(t.messages, assistantCommentaryMessage(n.id, noticeText(n)))
	}
	return t
}

// retain records the exact router-authored IDs before they can become visible.
// Failure suppresses only these auxiliary notices; finish leaves them queued
// because emitted remains false.
func (t *criticalErrorTransform) retain(ctx context.Context, store *mekugiReplayStore, workspace string) {
	if t == nil || store == nil || len(t.messages) == 0 {
		return
	}
	ids := make([]string, 0, len(t.messages))
	for _, message := range t.messages {
		ids = append(ids, jsonString(message, "id"))
	}
	if store.putCommentary(ctx, workspace, ids) != nil {
		t.messages = nil
	}
}

func (t *criticalErrorTransform) suppress() {
	if t != nil {
		t.messages = nil
	}
}

func (t *criticalErrorTransform) finish(success bool) {
	if t == nil {
		return
	}
	t.owner.mu.Lock()
	defer t.owner.mu.Unlock()
	for i, n := range t.notices {
		n.inFlight = false
		if success && t.emitted {
			n.delivered = t.counts[i]
		}
	}
}

func (t *criticalErrorTransform) TransformJSON(body []byte) ([]byte, error) {
	if t == nil || len(t.messages) == 0 {
		return body, nil
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(body, &object) != nil {
		return body, nil
	}
	var output []map[string]json.RawMessage
	if json.Unmarshal(object["output"], &output) != nil {
		return body, nil
	}
	object["output"] = mustMarshalJSON(append(slices.Clone(t.messages), output...))
	result, err := marshalProtocolJSON(object)
	if err == nil {
		t.emitted = true
	}
	return result, err
}

func (t *criticalErrorTransform) TransformSSE(body []byte) ([][]byte, error) {
	if t == nil || len(t.messages) == 0 {
		return [][]byte{body}, nil
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(body, &object) != nil {
		return [][]byte{body}, nil
	}
	kind := responseevents.Kind(jsonString(object, "type"))
	switch {
	case kind == responseevents.Created:
		// A child's last standalone assistant item must remain its substantive result.
		if !t.subagent {
			result := [][]byte{body}
			for _, message := range t.messages {
				result = append(result, assistantCommentaryDoneEvent(message))
			}
			t.emitted = true
			return result, nil
		}
	case kind.Terminal():
		response, err := t.TransformJSON(object["response"])
		if err != nil {
			return nil, err
		}
		result, err := replaceRawField(body, "response", response)
		return [][]byte{result}, err
	}
	return [][]byte{body}, nil
}
func (*criticalErrorTransform) Finish(bool) error { return nil }

// Permanent local incompatibilities must not masquerade as retryable upstream 502s.
type requestCompatibilityError struct{ code, message string }

func (e *requestCompatibilityError) Error() string { return "Mekugi " + e.code + ": " + e.message }
func incompatibleRequest(code, message string) error {
	return &requestCompatibilityError{code: code, message: message}
}
