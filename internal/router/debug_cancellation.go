package router

import (
	"context"
	"errors"
)

var (
	errRouterShutdown       = errors.New("router shutdown")
	errResponseStartTimeout = errors.New("response start timeout")
)

// A downstream context cancellation is not proof of an explicit user abort.
// Unknown causes stay unknown. Cleanup cancels run after request finalization.
func requestCancellationCause(execution context.Context, err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, context.Canceled) && errors.Is(context.Cause(execution), errRouterShutdown):
		return "router_shutdown"
	case errors.Is(err, context.DeadlineExceeded) && errors.Is(err, errResponseStartTimeout):
		return "response_start_timeout"
	case errors.Is(err, errDownstreamDisconnected):
		return "downstream_disconnected"
	case errors.Is(err, context.Canceled) && errors.Is(execution.Err(), context.Canceled):
		return "downstream_context_canceled"
	case errors.Is(err, context.DeadlineExceeded) && errors.Is(execution.Err(), context.DeadlineExceeded):
		return "downstream_deadline_exceeded"
	case errors.Is(err, errUpstreamStreamIdleTimeout):
		return "upstream_idle_timeout"
	case errors.Is(err, context.Canceled):
		return "cancellation_unknown"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_unknown"
	}
	return ""
}

// Propagate only our owned cause, not arbitrary parent cancellation text.
func withRequestStartCause(ctx context.Context, err error) error {
	if err != nil && ctx.Err() != nil && errors.Is(err, ctx.Err()) && errors.Is(context.Cause(ctx), errResponseStartTimeout) {
		return errors.Join(err, errResponseStartTimeout)
	}
	return err
}
