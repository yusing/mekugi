package router

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type grokClient struct {
	httpClient        *http.Client
	auth              *grokAuth
	streamIdleTimeout time.Duration
}

func (g *grokClient) forwardExecution(startCtx, responseCtx context.Context, body []byte, headers http.Header) (*http.Response, error) {
	if _, _, err := requiredCodexAuthHeaders(headers); err != nil {
		return nil, err
	}
	tr, err := translateGrokRequest(body)
	if err != nil {
		return nil, err
	}
	credentials, err := g.auth.credentials(startCtx)
	if err != nil {
		return nil, err
	}
	endpoint := credentials.endpoint
	encoded, err := json.Marshal(tr.body)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(responseCtx)
	stopStart := context.AfterFunc(startCtx, cancel)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		stopStart()
		cancel()
		return nil, errors.New("invalid Grok endpoint")
	}
	// Never forward Codex's Authorization, account ID, session ID, or internal
	// headers. Each provider gets only credentials issued for its own endpoint.
	request.Header = credentials.headers.Clone()
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	response, err := g.httpClient.Do(request)
	if err != nil {
		stopStart()
		cancel()
		return nil, fmt.Errorf("Grok request failed: %w", err)
	}
	if response.StatusCode == http.StatusUnauthorized && g.auth.apiKey == "" {
		response.Body.Close()
		if _, err := g.auth.refresh(startCtx, strings.TrimPrefix(credentials.headers.Get("Authorization"), "Bearer ")); err != nil {
			stopStart()
			cancel()
			return nil, err
		}
		credentials, err = g.auth.credentials(startCtx)
		if err != nil {
			stopStart()
			cancel()
			return nil, err
		}
		retry, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
		if err != nil {
			stopStart()
			cancel()
			return nil, err
		}
		retry.Header = credentials.headers.Clone()
		retry.Header.Set("Content-Type", "application/json")
		retry.Header.Set("Accept", "text/event-stream")
		response, err = g.httpClient.Do(retry)
		if err != nil {
			stopStart()
			cancel()
			return nil, errors.New("Grok retry after authentication refresh failed")
		}
	}
	if !stopStart() {
		response.Body.Close()
		cancel()
		return nil, startCtx.Err()
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		response.Body.Close()
		cancel()
		// Error bodies can echo inputs or credentials. Return only status and a
		// bounded router-owned diagnostic, never raw provider authentication errors.
		return nil, fmt.Errorf("Grok returned HTTP %d; check Grok access and authentication", response.StatusCode)
	}
	var upstream io.ReadCloser = &cancelOnCloseReadCloser{body: response.Body, cancel: cancel}
	if g.streamIdleTimeout > 0 {
		upstream = newStreamIdleReadCloser(ctx, upstream, g.streamIdleTimeout)
	}
	if !tr.stream {
		result, err := tr.readGrokStream(upstream, func(map[string]any) error { return nil })
		if err != nil {
			upstream.Close()
			return nil, err
		}
		data := mustMarshalJSON(result)
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: &grokResponseBody{Reader: bytes.NewReader(data), close: upstream.Close}}, nil
	}
	reader, writer := io.Pipe()
	stopCancel := context.AfterFunc(ctx, func() { writer.CloseWithError(ctx.Err()) })
	var streamErr error
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		_, err := tr.readGrokStream(upstream, func(event map[string]any) error {
			_, err := fmt.Fprintf(writer, "event: %s\ndata: %s\n\n", event["type"], mustMarshalJSON(event))
			return err
		})
		streamErr = err
		writer.CloseWithError(err)
	}()
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: &grokResponseBody{Reader: reader, terminalError: func() error {
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

type grokResponseBody struct {
	io.Reader
	close         func() error
	terminalError func() error
}

func (b *grokResponseBody) Close() error { return b.close() }

func isGrokModel(model string) bool { return strings.HasPrefix(model, "grok:") }
