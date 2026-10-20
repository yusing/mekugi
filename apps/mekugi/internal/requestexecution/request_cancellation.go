// Source: apps/mekugi/internal/router/request_cancellation.go:1:39@b9679c93.
package requestexecution

import (
	"context"
	"errors"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"

	"github.com/yusing/mekugi/libraries/httptransport"
)

// A downstream context cancellation is not proof of an explicit user abort.
// Unknown causes stay unknown. Cleanup cancels run after request finalization.
func requestCancellationCause(execution context.Context, err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, context.Canceled) && errors.Is(context.Cause(execution), inferencegateway.ErrShutdown):
		return "router_shutdown"
	case errors.Is(err, context.DeadlineExceeded) && errors.Is(err, inferencegateway.ErrStartTimeout):
		return "response_start_timeout"
	case errors.Is(err, codexresponses.ErrDownstreamDisconnected):
		return "downstream_disconnected"
	case errors.Is(err, context.Canceled) && errors.Is(execution.Err(), context.Canceled):
		return "downstream_context_canceled"
	case errors.Is(err, context.DeadlineExceeded) && errors.Is(execution.Err(), context.DeadlineExceeded):
		return "downstream_deadline_exceeded"
	case errors.Is(err, httptransport.ErrStreamIdleTimeout):
		return "upstream_idle_timeout"
	case errors.Is(err, context.Canceled):
		return "cancellation_unknown"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_unknown"
	}
	return ""
}
