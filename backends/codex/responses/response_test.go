package responses

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/yusing/mekugi/libraries/httptransport"
)

type cancelOrderBody struct {
	ctx               context.Context
	reading, closed   chan struct{}
	once              sync.Once
	closeBeforeCancel bool
}

func (b *cancelOrderBody) Read([]byte) (int, error) {
	close(b.reading)
	<-b.closed
	return 0, io.ErrClosedPipe
}

func (b *cancelOrderBody) Close() error {
	b.once.Do(func() {
		b.closeBeforeCancel = b.ctx.Err() == nil
		close(b.closed)
	})
	return nil
}

func TestResponseCloseCancelsNestedRequestBeforeSource(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		request, err := testTranslate([]byte(`{"input":[]}`))
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		source := &cancelOrderBody{reading: make(chan struct{}), closed: make(chan struct{})}
		upstream, err := httptransport.Exchange(t.Context(), ctx, 0, func(ctx context.Context) (*http.Response, error) {
			source.ctx = ctx
			return &http.Response{StatusCode: http.StatusOK, Body: source}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		response, err := request.codec.Response(ctx, cancel, upstream.Body, true, testFailureDetail)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := response.Body.Read(make([]byte, 4096)); err != nil {
			t.Fatal(err)
		}
		<-source.reading
		if err := response.Body.Close(); err != nil {
			t.Fatal(err)
		}
		if source.closeBeforeCancel {
			t.Fatal("source closed before nested request cancellation")
		}
	})
}

func TestResponseNestedIdleTimeoutDeliversFailedTerminalAndCause(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fields, err := testParse([]byte(`{"input":[]}`))
		if err != nil {
			t.Fatal(err)
		}
		policy := testPolicy()
		policy.Diagnostics.ForwardError = func(err error) error {
			return errors.Join(&testDiagnostic{code: "fixture_transport", summary: "fixture transport failed"}, err)
		}
		_, stream, err := Translate(fields, "fixture-model", "chat", policy)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		reader, writer := io.Pipe()
		defer writer.Close()
		upstream, err := httptransport.Exchange(t.Context(), ctx, time.Second, func(context.Context) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: reader}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		response, err := stream.Response(ctx, cancel, upstream.Body, true, testFailureDetail)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if !bytes.Contains(data, []byte("event: response.failed\n")) || !errors.Is(err, httptransport.ErrStreamIdleTimeout) {
			t.Fatalf("response body = %s, error = %v; want failed terminal and idle timeout", data, err)
		}
		if err := response.Body.(*ResponseBody).TerminalError(); !errors.Is(err, httptransport.ErrStreamIdleTimeout) {
			t.Fatalf("terminal error = %v, want idle timeout", err)
		}
		if err := ctx.Err(); err != nil {
			t.Fatalf("inner timeout canceled outer bridge before terminal delivery: %v", err)
		}
	})
}
