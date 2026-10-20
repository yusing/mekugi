package requestexecution

import (
	"context"
	"errors"
	"io"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/httptransport"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"
)

var ErrMissingTerminal = errors.New("upstream Responses response ended without a terminal state")

type requestFailurePhase string

const (
	requestFailurePrepare            requestFailurePhase = "prepare"
	requestFailureForward            requestFailurePhase = "forward"
	requestFailureModels             requestFailurePhase = "models"
	requestFailureInspectResponse    requestFailurePhase = "inspect_response"
	requestFailureStreamIdleTimeout  requestFailurePhase = "stream_idle_timeout"
	requestFailureTransform          requestFailurePhase = "transform"
	requestFailureWriteResponse      requestFailurePhase = "write_response"
	requestFailureTerminalValidation requestFailurePhase = "terminal_validation"
)

type requestFinalization struct {
	observation           requestObservation
	sessionID             string
	threadID              string
	turnID                string
	diagnosticMessage     string
	diagnosticError       string
	streamDiagnostics     *transportcapture.Stream
	failurePhase          requestFailurePhase
	upstreamStatusCode    int
	providerFailure       error
	diagnosticReference   string
	diagnosticCode        string
	upstreamTerminalState responses.TerminalState
}

func (f *requestFinalization) finish(
	ctx context.Context,
	requestErr error,
	output io.Writer,
	issues *Failures,
) {
	responseStarted := requestResponseStarted(output)
	if f.observation.outcome == requestOutcomeUnknown {
		switch {
		case errors.Is(requestErr, context.DeadlineExceeded):
			f.observation.outcome = requestOutcomeTimedOut
		case errors.Is(requestErr, codexresponses.ErrDownstreamDisconnected),
			errors.Is(requestErr, context.Canceled) && errors.Is(ctx.Err(), context.Canceled):
			if errors.Is(requestErr, httptransport.ErrStreamIdleTimeout) {
				f.failurePhase = requestFailureInspectResponse
			}
			if responseStarted {
				f.observation.outcome = requestOutcomeCanceledAfterResponse
			} else {
				f.observation.outcome = requestOutcomeCanceledBeforeResponse
			}
		case errors.Is(requestErr, httptransport.ErrStreamIdleTimeout):
			f.observation.outcome = requestOutcomeStreamIdleTimedOut
		default:
			f.observation.outcome = requestOutcomeFailed
		}
	}
	if f.observation.outcome == requestOutcomeCompleted || f.observation.outcome == requestOutcomeCanceledBeforeResponse || f.observation.outcome == requestOutcomeCanceledAfterResponse {
		return
	}
	if requestErr == nil {
		requestErr = f.providerFailure
	}
	diagnostic := issues.Report(Failure{Session: f.sessionID, Thread: f.threadID, Turn: f.turnID,
		Phase: string(f.failurePhase), UpstreamStatus: f.upstreamStatusCode,
		Terminal: f.upstreamTerminalState, Stream: f.streamDiagnostics}, requestErr)
	f.diagnosticCode, f.diagnosticReference = diagnostic.Code, diagnostic.Reference
	f.diagnosticError, f.diagnosticMessage = diagnostic.Error, diagnostic.Message
}

func requestResponseStarted(output io.Writer) bool {
	writer, ok := output.(interface{ ResponseStarted() bool })
	return ok && writer.ResponseStarted()
}

func (f *requestFinalization) classifyCopyError(err error) {
	switch {
	case errors.Is(err, httptransport.ErrStreamIdleTimeout):
		f.failurePhase = requestFailureStreamIdleTimeout
	case errors.Is(err, codexresponses.ErrTransform):
		f.failurePhase = requestFailureTransform
	case errors.Is(err, codexresponses.ErrWrite):
		f.failurePhase = requestFailureWriteResponse
	default:
		f.failurePhase = requestFailureInspectResponse
	}
}
