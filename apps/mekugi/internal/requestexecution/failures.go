// Source: apps/mekugi/internal/router/critical_errors.go:1:316@b9679c93.
package requestexecution

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"syscall"

	"github.com/yusing/mekugi/backends/codex/binding"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"

	"github.com/yusing/mekugi/modules/notifications/notice"

	"github.com/yusing/mekugi/modules/diagnostics/diagnostic"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	responseevents "github.com/yusing/mekugi/libraries/modelprotocol/responses"

	"github.com/yusing/mekugi/libraries/httptransport"

	"github.com/yusing/mekugi/modules/sessionhistory/history"

	"github.com/coder/websocket"
	"github.com/yusing/mekugi/libraries/persistence"
)

// Failures adapts request classification and private failure publication
// to an independently optional notice owner. It holds no presentation state.
type Failures struct {
	Notices        *notice.Service
	diagnostics    *diagnostic.Service
	failureLimits  history.Limits
	writes         *persistence.Counter
	mu             sync.Mutex
	diagnosticSalt string
	failureStore   *history.Store
}

func NewFailures(notices *notice.Service) *Failures {
	return &Failures{Notices: notices, diagnosticSalt: rand.Text()}
}

// DiagnosticError keeps a stable classification alongside the underlying
// error. Notices and failure records include the complete error string.
type DiagnosticError struct {
	Cause        error
	Code         string
	Summary      string
	CallerDetail string
	Distinct     bool
}

func (e *DiagnosticError) Error() string {
	if e.CallerDetail != "" {
		return e.CallerDetail
	}
	return e.Cause.Error()
}
func (e *DiagnosticError) Unwrap() error { return e.Cause }

func Classify(err error, code, summary string, distinct bool) error {
	if err == nil {
		return nil
	}
	if _, ok := errors.AsType[*DiagnosticError](err); ok {
		return err
	}
	return &DiagnosticError{Cause: err, Code: code, Summary: summary, Distinct: distinct}
}

// Classify wrapped transport errors with stable codes and actionable summaries.
// Complete error detail remains available to notices and private diagnostics.
func ForwardError(err error) error {
	if _, ok := errors.AsType[*inferencegateway.CompatibilityError](err); ok {
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
	if rejection, ok := errors.AsType[*inferencegateway.WebSocketStatusError](err); ok {
		code = fmt.Sprintf("upstream_websocket_http_%d", rejection.Status)
		summary = fmt.Sprintf("the upstream WebSocket handshake was rejected with HTTP %d", rejection.Status)
	}
	if rejection, ok := errors.AsType[*inferencegateway.ProviderHTTPError](err); ok {
		code = fmt.Sprintf("upstream_provider_http_%d", rejection.Status)
		summary = fmt.Sprintf("the provider rejected the request with HTTP %d", rejection.Status)
	}
	return Classify(err, code, summary, true)
}

func StaticError(code, summary string) error {
	return &DiagnosticError{Cause: errors.New(summary), Code: code, Summary: summary}
}

func (c *Failures) diagnosticReference(f *requestFinalization, err error) string {
	mac := hmac.New(sha256.New, []byte(c.diagnosticSalt))
	_, _ = fmt.Fprintf(mac, "%s|%d|%s|", f.failurePhase, f.upstreamStatusCode, f.upstreamTerminalState)
	if err == nil {
		_, _ = mac.Write([]byte("request failed without a wrapped error"))
	} else {
		_, _ = mac.Write([]byte(err.Error()))
	}
	return fmt.Sprintf("%x", mac.Sum(nil)[:6])
}

func (c *Failures) record(f *requestFinalization, err error) {
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
	if diagnostic, ok := errors.AsType[*DiagnosticError](err); ok {
		f.diagnosticCode = diagnostic.Code
	}
	if compatibility, ok := errors.AsType[*inferencegateway.CompatibilityError](err); ok {
		f.diagnosticCode = compatibility.Code
	}
	category := string(f.failurePhase)
	message := "Mekugi could not complete the request. Retry the turn; if it persists, restart the session."
	if diagnostic, ok := errors.AsType[*DiagnosticError](err); ok && diagnostic.CallerDetail != "" {
		category, message = diagnostic.Code+":"+f.diagnosticReference, diagnostic.CallerDetail
	} else if compatibility, ok := errors.AsType[*inferencegateway.CompatibilityError](err); ok {
		category, message = compatibility.Code, compatibility.Error()
	} else if rejection, ok := errors.AsType[*inferencegateway.ProviderHTTPError](err); ok {
		category, message = "provider_http_error:"+f.diagnosticReference, rejection.Error()
	} else {
		switch {
		case f.failurePhase == requestFailureModels:
			message = "Mekugi could not refresh the model catalog. Existing model turns may still work."
		case f.upstreamStatusCode == 401 || f.upstreamStatusCode == 403:
			category, message = "authentication", "Mekugi upstream authentication was rejected. Check your Codex or Grok credentials before retrying."
		case f.upstreamStatusCode == 429:
			category, message = "rate_limit", "The upstream service rate-limited this turn. Wait before retrying."
		case errors.Is(err, context.DeadlineExceeded), errors.Is(err, httptransport.ErrStreamIdleTimeout):
			category, message = "timeout", "The upstream response timed out. Retry the turn."
		case f.failurePhase == requestFailurePrepare:
			message = "Mekugi could not prepare this request. Check the session's tool and configuration compatibility before retrying."
		case f.failurePhase == requestFailureTransform:
			message = "Mekugi could not safely translate the response. Retrying will fail the same way. Switch model or relaunch with --debug and report the diagnostic reference."
		}
		if category == string(f.failurePhase) {
			reference := f.diagnosticReference
			phase := requestFailureDescription(f.failurePhase)
			diagnostic, _ := errors.AsType[*DiagnosticError](err)
			switch {
			case diagnostic != nil:
			case errors.Is(err, ErrMissingTerminal):
				state := f.upstreamTerminalState.String()
				diagnostic = &DiagnosticError{Code: "missing_upstream_terminal:" + state, Summary: "the upstream response ended without a completed or failed terminal state; observed state was " + state}
			case errors.Is(err, codexresponses.ErrWrite), f.failurePhase == requestFailureWriteResponse:
				diagnostic = &DiagnosticError{Code: "downstream_response_write", Summary: "the downstream response could not be written", Distinct: true}
			case f.failurePhase == requestFailureTerminalValidation && err == nil && f.upstreamTerminalState != responseevents.TerminalUnknown:
				diagnostic = &DiagnosticError{Code: "upstream_" + f.upstreamTerminalState.String(), Summary: "the upstream response reported terminal state " + f.upstreamTerminalState.String()}
			}
			message += " Failure phase: " + phase + "."
			if diagnostic != nil {
				f.diagnosticCode = diagnostic.Code
				category += ":" + diagnostic.Code
				if diagnostic.Distinct {
					category += ":" + reference
				}
				message += " Cause: " + diagnostic.Code + ": " + diagnostic.Summary + ". Diagnostic reference: " + reference + "."
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
	c.Notices.Submit(notice.Input{
		ID:      noticeID("critical:" + f.sessionID + ":" + category),
		Session: f.sessionID, Thread: f.threadID, Turn: f.turnID,
		Category: category, Reference: f.diagnosticReference, Text: message, Request: true,
	})
}

func (c *Failures) ObserveTurnCompletion(method string, params []byte) {
	if c == nil || method != "turn/completed" {
		return
	}
	var event binding.Notification
	if json.Unmarshal(params, &event) != nil {
		return
	}
	c.Notices.ObserveTurn(event.ThreadID, event.Turn.ID, event.Turn.Status)
}

func (c *Failures) AddNotice(session, category, message string) {
	c.AddThreadNotice(session, "", category, message)
}

func (c *Failures) AddThreadNotice(session, thread, category, message string) {
	if c == nil {
		return
	}
	c.Notices.Submit(notice.Input{ID: noticeID("notice:" + session + ":" + thread + ":" + category), Session: session, Thread: thread, Category: category, Text: message})
}

// Cleanup supplies a typed surface at the producer boundary, rather than asking
// a renderer to infer presentation from a category string.
func (c *Failures) CleanupNotice(session, thread, phase, message string) {
	if c == nil {
		return
	}
	category := "storage_cleanup_" + phase
	surface := notice.Error
	if phase == "planning" || phase == "reclaimed" {
		surface = notice.Progress
	}
	c.Notices.Submit(notice.Input{ID: noticeID("notice:" + session + ":" + thread + ":" + category), Session: session, Thread: thread, Category: category, Text: message, Surface: surface})
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

func noticeID(seed string) string {
	digest := sha256.Sum256([]byte(seed))
	return fmt.Sprintf("msg_mekugi_commentary_%x", digest[:12])
}
