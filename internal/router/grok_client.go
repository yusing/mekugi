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
	openCode          *openCodeService
	auth              *grokAuth
	streamIdleTimeout time.Duration
}

func (g *grokClient) forwardExecution(startCtx, responseCtx context.Context, body []byte, headers http.Header) (*http.Response, error) {
	if _, _, err := requiredCodexAuthHeaders(headers); err != nil {
		return nil, err
	}
	service := g.openCode
	if service != nil {
		if service.catalog != nil {
			// Refresh metadata only, never retry an inference request.
			_ = service.catalog.refresh(startCtx, false)
		}
		pinned := service.pin()
		service = &pinned
	}
	tr, err := translateChatRequest(body, service)
	if err != nil {
		return nil, err
	}
	label := "Grok"
	var credentials grokCredentials
	if g.openCode != nil {
		label = g.openCode.label
		credentials = grokCredentials{endpoint: g.openCode.endpoint, headers: http.Header{
			"Authorization": {"Bearer " + g.openCode.apiKey}, "User-Agent": {"mekugi"},
		}}
		if session := openCodeSessionID(g.openCode.prefix, headers.Get(threadIDHeader)); session != "" {
			credentials.headers.Set("x-opencode-session", session)
		}
		switch tr.format {
		case "anthropic":
			credentials.endpoint = strings.TrimSuffix(g.openCode.endpoint, "/chat/completions") + "/messages"
			credentials.headers.Del("Authorization")
			credentials.headers.Set("x-api-key", g.openCode.apiKey)
			credentials.headers.Set("anthropic-version", "2023-06-01")
		case "responses":
			credentials.endpoint = strings.TrimSuffix(g.openCode.endpoint, "/chat/completions") + "/responses"
		}
	} else {
		credentials, err = g.auth.credentials(startCtx)
		if err != nil {
			return nil, err
		}
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
		return nil, fmt.Errorf("invalid %s endpoint", label)
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
		return nil, fmt.Errorf("%s request failed: %w", label, forwardCriticalDiagnostic(err))
	}
	if response.StatusCode == http.StatusUnauthorized && g.openCode == nil && g.auth.apiKey == "" {
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
			return nil, errors.New("grok retry after authentication refresh failed")
		}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		defer stopStart()
		defer cancel()
		body := newStreamIdleReadCloser(ctx, response.Body, 5*time.Second)
		defer body.Close()
		// Bound the complete error read, including a provider that drips bytes.
		timer := time.AfterFunc(5*time.Second, func() { _ = body.Close() })
		defer timer.Stop()
		detail, readErr := io.ReadAll(io.LimitReader(body, maxUpstreamErrorDetailBytes+1))
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if readErr != nil {
			detail = []byte("provider error body could not be read")
		} else if len(detail) > maxUpstreamErrorDetailBytes {
			detail = []byte("provider error body exceeds the 8 KiB limit")
		}
		return nil, newProviderHTTPError(label, response.StatusCode, detail, credentials.headers, headers)
	}
	if !stopStart() {
		response.Body.Close()
		cancel()
		return nil, startCtx.Err()
	}
	var upstream io.ReadCloser = &cancelOnCloseReadCloser{body: response.Body, cancel: cancel}
	if g.streamIdleTimeout > 0 {
		upstream = newStreamIdleReadCloser(ctx, upstream, g.streamIdleTimeout)
	}
	var price *openCodePrice
	if service != nil && service.snapshot != nil {
		model, _ := tr.body["model"].(string)
		price = new(service.snapshot.Models[service.prefix][model].Cost)
	}
	tr.providerFailureDetail = func(body []byte) string {
		return newProviderHTTPError(label, 0, body, credentials.headers, headers).Error()
	}
	if !tr.stream {
		result, err := tr.readProviderStream(upstream, func(map[string]any) error { return nil })
		if err != nil {
			upstream.Close()
			return nil, err
		}
		data := mustMarshalJSON(result)
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: &grokResponseBody{Reader: bytes.NewReader(data), close: upstream.Close, openCodePrice: price}}, nil
	}
	reader, writer := io.Pipe()
	stopCancel := context.AfterFunc(ctx, func() { writer.CloseWithError(ctx.Err()) })
	var streamErr error
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		_, err := tr.readProviderStream(upstream, func(event map[string]any) error {
			_, err := fmt.Fprintf(writer, "event: %s\ndata: %s\n\n", event["type"], mustMarshalJSON(event))
			return err
		})
		streamErr = err
		writer.CloseWithError(err)
	}()
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: &grokResponseBody{Reader: reader, openCodePrice: price, terminalError: func() error {
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
	openCodePrice *openCodePrice
	terminalError func() error
}

func (b *grokResponseBody) Close() error { return b.close() }

func isGrokModel(model string) bool { return strings.HasPrefix(model, "grok:") }
