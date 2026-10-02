package router

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"syscall"

	"github.com/coder/websocket"
	"github.com/yusing/mekugi/internal/persistence"
)

// CriticalErrors retains bounded, actionable session notices with complete error text,
// but no request snapshots or operational event history. It outlives router shutdown so
// the launcher can report notices that could not reach Codex.
type CriticalErrors struct {
	writes          *persistence.Counter
	mu              sync.Mutex
	entries         []*criticalNotice
	overflow        uint64
	diagnosticSalt  string
	failureStore    *mekugiReplayStore
	persistFailures bool
}

type criticalNotice struct {
	session, category, message, id string
	thread, turn, reference        string
	count, delivered               uint64
	inFlight                       bool
}

func NewCriticalErrors() *CriticalErrors { return &CriticalErrors{diagnosticSalt: rand.Text()} }

// criticalDiagnosticError keeps a stable classification alongside the underlying
// error. Notices and failure records include the complete error string.
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
	if err != nil {
		f.diagnosticError = err.Error()
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
		case f.failurePhase == requestFailureModels:
			message = "Mekugi could not refresh the model catalog. Existing model turns may still work."
		case f.upstreamStatusCode == 401 || f.upstreamStatusCode == 403:
			category, message = "authentication", "Mekugi upstream authentication was rejected. Check your Codex or Grok credentials before retrying."
		case f.upstreamStatusCode == 429:
			category, message = "rate_limit", "The upstream service rate-limited this turn. Wait before retrying."
		case errors.Is(err, context.DeadlineExceeded), errors.Is(err, errUpstreamStreamIdleTimeout):
			category, message = "timeout", "The upstream response timed out. Retry the turn."
		case f.failurePhase == requestFailurePrepare:
			message = "Mekugi could not prepare this request. Check the session's tool and configuration compatibility before retrying."
		case f.failurePhase == requestFailureTransform:
			message = "Mekugi could not safely translate the response. Retrying will fail the same way. Switch model, use passthrough mode, or relaunch with --debug and report the diagnostic reference."
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
				message += " Cause: " + diagnostic.code + ": " + diagnostic.summary + ". Diagnostic reference: " + reference + "."
			} else {
				category += ":unclassified:" + reference
				message += " Diagnostic reference: " + reference + "."
			}
		} else {
			if f.diagnosticCode == "unclassified" {
				f.diagnosticCode = category
			}
			category += ":" + f.diagnosticReference
			message += " Diagnostic reference: " + f.diagnosticReference + "."
		}
	}
	if f.diagnosticError != "" && !strings.Contains(message, f.diagnosticError) {
		message += " Error: " + f.diagnosticError
	}
	f.diagnosticMessage = message
	// Catalog discovery is auxiliary. Preserve diagnostics and the HTTP error
	// for Codex, but do not interrupt model turns with a terminal notice.
	if f.failurePhase == requestFailureModels {
		return
	}
	if f.turnID != "" {
		category += ":" + f.threadID + ":" + f.turnID
	}
	noticeID := commentaryMessageID("critical:" + f.sessionID + ":" + category)
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, notice := range c.entries {
		if f.turnID != "" && notice.thread == f.threadID && notice.turn == f.turnID && notice.reference == f.diagnosticReference {
			f.diagnosticNotice = notice
			return
		}
	}
	for _, notice := range c.entries {
		if notice.session == f.sessionID && notice.category == category {
			notice.count++
			notice.thread, notice.turn, notice.reference = f.threadID, f.turnID, f.diagnosticReference
			f.diagnosticNotice = notice
			return
		}
	}
	if len(c.entries) >= 256 {
		c.overflow++
		return
	}
	f.diagnosticNotice = &criticalNotice{session: f.sessionID, category: category, message: message,
		thread: f.threadID, turn: f.turnID, reference: f.diagnosticReference,
		id: noticeID, count: 1}
	c.entries = append(c.entries, f.diagnosticNotice)
}

// Auxiliary degradation and automatic cleanup are user-visible without turning
// successful work into a failed request. Messages here are producer-owned text,
// not arbitrary request data. Empty session denotes a router-wide notice.
func (c *CriticalErrors) addNotice(session, category, message string) {
	c.addThreadNotice(session, "", category, message)
}

func (c *CriticalErrors) addThreadNotice(session, thread, category, message string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, notice := range c.entries {
		if notice.session == session && notice.thread == thread && notice.category == category {
			notice.count++
			return
		}
	}
	if len(c.entries) >= 256 {
		c.overflow++
		return
	}
	c.entries = append(c.entries, &criticalNotice{
		session: session, thread: thread, category: category, message: message,
		id: commentaryMessageID("notice:" + session + ":" + thread + ":" + category), count: 1,
	})
}

func requestFailureDescription(phase requestFailurePhase) string {
	switch phase {
	case requestFailurePrepare:
		return "request preparation"
	case requestFailureForward:
		return "upstream request forwarding"
	case requestFailureModels:
		return "model catalog refresh"
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

// criticalNoticeDelivery reserves native display notices until the terminal write
// succeeds. It never modifies provider responses or model-visible history.
type criticalNoticeDelivery struct {
	owner     *CriticalErrors
	notices   []*criticalNotice
	snapshots []criticalNotice
}

func (c *CriticalErrors) takeNative(root string, activity *subagentActivity) *criticalNoticeDelivery {
	if c == nil || root == "" {
		return nil
	}
	// Snapshot ancestry before locking the notice queue: activity producers may
	// enqueue capacity notices while holding their own lock.
	scopedThreads := map[string]bool{root: true}
	if activity != nil {
		activity.mu.Lock()
		if node := activity.threads[root]; node != nil && activity.rootLocked(root) != root {
			activity.mu.Unlock()
			return nil
		}
		for thread := range activity.threads {
			if activity.rootLocked(thread) == root {
				scopedThreads[thread] = true
			}
		}
		activity.mu.Unlock()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delivery := &criticalNoticeDelivery{owner: c}
	for _, notice := range c.entries {
		if notice.delivered != 0 || notice.inFlight {
			continue
		}
		scoped := scopedThreads[notice.thread] || notice.thread == "" && notice.session == ""
		if !scoped {
			continue
		}
		notice.inFlight = true
		delivery.notices = append(delivery.notices, notice)
		delivery.snapshots = append(delivery.snapshots, *notice)
	}
	if len(delivery.notices) == 0 {
		return nil
	}
	return delivery
}

func (d *criticalNoticeDelivery) finish(success bool) {
	if d == nil {
		return
	}
	d.owner.mu.Lock()
	defer d.owner.mu.Unlock()
	for i, notice := range d.notices {
		notice.inFlight = false
		if success {
			notice.delivered = d.snapshots[i].count
		}
	}
}

type requestCompatibilityError struct{ code, message string }

func (e *requestCompatibilityError) Error() string { return "Mekugi " + e.code + ": " + e.message }
func incompatibleRequest(code, message string) error {
	return &requestCompatibilityError{code: code, message: message}
}
