package inferencegateway

// Source: apps/mekugi/internal/router/server.go:584:593@[bd606c09] request contexts.
// Source: apps/mekugi/internal/router/request_cancellation.go:12:50@[bd606c09] owned cancellation causes.

import (
	"context"
	"errors"
	"time"
)

var (
	ErrShutdown     = errors.New("router shutdown")
	ErrStartTimeout = errors.New("response start timeout")
)

// RequestContexts separates response-start admission from response consumption.
func RequestContexts(requestCtx, serverCtx context.Context, responseStartTimeout time.Duration) (context.Context, context.Context, func()) {
	executionCtx, cancelExecution := context.WithCancelCause(requestCtx)
	stopServerCancellation := context.AfterFunc(serverCtx, func() { cancelExecution(ErrShutdown) })
	startCtx, cancelStart := context.WithTimeoutCause(executionCtx, responseStartTimeout, ErrStartTimeout)
	return startCtx, executionCtx, func() { stopServerCancellation(); cancelStart(); cancelExecution(nil) }
}

// Propagate only our owned cause, not arbitrary parent cancellation text.
func WithRequestStartCause(ctx context.Context, err error) error {
	if err != nil && ctx.Err() != nil && errors.Is(err, ctx.Err()) && errors.Is(context.Cause(ctx), ErrStartTimeout) {
		return errors.Join(err, ErrStartTimeout)
	}
	return err
}
