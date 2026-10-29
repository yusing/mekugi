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

// NewRequest builds a replayable request over immutable encoded bytes. The
// caller must not mutate body while the transport owns the request body.
func NewRequest(ctx context.Context, method, url string, body []byte) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil || request.Body == http.NoBody {
		return request, err
	}
	request.Body = &immutableRequestBody{Reader: bytes.NewReader(body), content: body}
	request.GetBody = func() (io.ReadCloser, error) {
		return &immutableRequestBody{Reader: bytes.NewReader(body), content: body}, nil
	}
	return request, nil
}

type immutableRequestBody struct {
	*bytes.Reader
	content []byte
}

func (*immutableRequestBody) Close() error { return nil }

// RequestBytes borrows an unread NewRequest body's bytes for synchronous,
// read-only observation. The caller must neither mutate nor retain the slice.
// Replaced or partially consumed bodies require ordinary stream observation.
func RequestBytes(request *http.Request) ([]byte, bool) {
	body, ok := request.Body.(*immutableRequestBody)
	if !ok || body.Len() != len(body.content) {
		return nil, false
	}
	return body.content, true
}

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
	timer     *time.Timer
	expired   chan struct{}
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
	if r.timer == nil {
		r.expired = make(chan struct{})
		r.timer = time.AfterFunc(r.timeout, func() {
			r.timedOut.Store(true)
			_ = r.closeBody()
			close(r.expired)
		})
	} else {
		r.timer.Reset(r.timeout)
	}
	read, err := r.body.Read(buffer)
	if !r.timer.Stop() {
		<-r.expired
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
