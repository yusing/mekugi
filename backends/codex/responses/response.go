package responses

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

// Source: internal/router/grok_client.go:173:215@8cd86549 JSON/SSE response bridge.

// Response adapts an accepted upstream body. It owns upstream and cancel on
// success or failure; streaming Close cancels before closing and joins production.
func (s *Stream) Response(ctx context.Context, cancel context.CancelFunc, upstream io.ReadCloser, stream bool, failureDetail func([]byte) string) (*http.Response, error) {
	if !stream {
		result, err := s.Read(upstream, func(map[string]any) error { return nil }, failureDetail)
		if err != nil {
			upstream.Close()
			cancel()
			return nil, err
		}
		data := responses.MustMarshalJSON(result)
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: &ResponseBody{Reader: bytes.NewReader(data), close: func() error {
			err := upstream.Close()
			cancel()
			return err
		}}}, nil
	}
	reader, writer := io.Pipe()
	stopCancel := context.AfterFunc(ctx, func() { writer.CloseWithError(ctx.Err()) })
	var streamErr error
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		_, err := s.Read(upstream, func(event map[string]any) error {
			_, err := fmt.Fprintf(writer, "event: %s\ndata: %s\n\n", event["type"], responses.MustMarshalJSON(event))
			return err
		}, failureDetail)
		streamErr = err
		writer.CloseWithError(err)
	}()
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: &ResponseBody{Reader: reader, terminalError: func() error {
		<-finished
		return streamErr
	}, close: func() error {
		stopCancel()
		reader.Close()
		cancel()
		err := upstream.Close()
		<-finished
		return err
	}}}, nil
}

// ResponseBody preserves a producer failure after delivery of its failed terminal.
type ResponseBody struct {
	io.Reader
	close         func() error
	terminalError func() error
}

func (b *ResponseBody) Close() error { return b.close() }

// TerminalError joins the producer. Call it only after terminal delivery, never
// while the producer can still be waiting for the consumer to read its output.
func (b *ResponseBody) TerminalError() error {
	if b.terminalError != nil {
		return b.terminalError()
	}
	return nil
}
