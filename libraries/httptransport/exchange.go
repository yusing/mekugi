package httptransport

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Source: internal/router/grok_client.go:88:163@8cd86549 final response admission.

// SendError distinguishes inference transport failures from credential failures.
type SendError struct{ Err error }

func (e *SendError) Error() string { return e.Err.Error() }
func (e *SendError) Unwrap() error { return e.Err }

// Rejection retains a final non-2xx response for the caller's error policy.
type Rejection struct {
	Status int
	Detail []byte
}

func (e *Rejection) Error() string { return fmt.Sprintf("upstream returned HTTP %d", e.Status) }

// Exchange admits the sender's final response, detaching successful body reads
// from startup cancellation. The sender owns credentials and retry policy.
func Exchange(startCtx, responseCtx context.Context, idleTimeout time.Duration, send func(context.Context) (*http.Response, error)) (*http.Response, error) {
	ctx, cancel := context.WithCancel(responseCtx)
	stopStart := context.AfterFunc(startCtx, cancel)
	response, err := send(ctx)
	if err != nil {
		stopStart()
		cancel()
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		defer stopStart()
		defer cancel()
		body := IdleRead(ctx, response.Body, 5*time.Second)
		defer body.Close()
		// Bound the complete read, including a provider that drips bytes.
		timer := time.AfterFunc(5*time.Second, func() { _ = body.Close() })
		defer timer.Stop()
		const detailLimit = 8 * 1024
		detail, readErr := io.ReadAll(io.LimitReader(body, detailLimit+1))
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if readErr != nil {
			detail = []byte(fmt.Sprintf("provider error body could not be read: %v", readErr))
		} else if len(detail) > detailLimit {
			detail = []byte("provider error body exceeds the 8 KiB limit")
		}
		return nil, &Rejection{Status: response.StatusCode, Detail: detail}
	}
	if !stopStart() {
		response.Body.Close()
		cancel()
		return nil, startCtx.Err()
	}
	response.Body = CancelOnClose(response.Body, cancel)
	if idleTimeout > 0 {
		response.Body = IdleRead(ctx, response.Body, idleTimeout)
	}
	return response, nil
}
