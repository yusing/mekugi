package httptransport

// Source: internal/router/client.go:267:327,424:429@49f759d6 response body lifetime and replay.
import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

var ErrStreamIdleTimeout = errors.New("upstream response stream idle timeout")

// CancelOnClose ties the request lifetime to its response body.
func CancelOnClose(body io.ReadCloser, cancel context.CancelFunc) io.ReadCloser {
	return &cancelOnCloseReadCloser{body: body, cancel: cancel}
}

type cancelOnCloseReadCloser struct {
	body   io.ReadCloser
	cancel context.CancelFunc
}

func (r *cancelOnCloseReadCloser) Read(body []byte) (int, error) {
	return r.body.Read(body)
}

func (r *cancelOnCloseReadCloser) Close() error {
	err := r.body.Close()
	r.cancel()
	return err
}

type streamIdleReadCloser struct {
	ctx       context.Context
	body      io.ReadCloser
	timeout   time.Duration
	timedOut  atomic.Bool
	closeOnce sync.Once
	closeErr  error
}

func IdleRead(ctx context.Context, body io.ReadCloser, timeout time.Duration) io.ReadCloser {
	return &streamIdleReadCloser{ctx: ctx, body: body, timeout: timeout}
}

func (r *streamIdleReadCloser) Read(buffer []byte) (int, error) {
	if r.timedOut.Load() {
		return 0, ErrStreamIdleTimeout
	}
	expired := make(chan struct{})
	timer := time.AfterFunc(r.timeout, func() {
		r.timedOut.Store(true)
		_ = r.closeBody()
		close(expired)
	})
	read, err := r.body.Read(buffer)
	if !timer.Stop() {
		<-expired
	}
	if r.timedOut.Load() {
		if contextErr := r.ctx.Err(); contextErr != nil {
			return read, errors.Join(contextErr, ErrStreamIdleTimeout, err)
		}
		return read, errors.Join(ErrStreamIdleTimeout, err)
	}
	return read, err
}

func (r *streamIdleReadCloser) closeBody() error {
	r.closeOnce.Do(func() {
		r.closeErr = r.body.Close()
	})
	return r.closeErr
}

func (r *streamIdleReadCloser) Close() error {
	return r.closeBody()
}

func RestorePrefix(response *http.Response, prefix []byte, remainder io.Reader) {
	response.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(prefix), remainder), response.Body}
}
