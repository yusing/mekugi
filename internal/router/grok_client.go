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

	"github.com/yusing/mekugi/providers/grok"
	"github.com/yusing/mekugi/providers/opencode"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

type grokClient struct {
	httpClient        *http.Client
	openCode          *opencode.Service
	auth              *grok.Auth
	unprefixed        bool
	streamIdleTimeout time.Duration
}

func (g *grokClient) forwardExecution(startCtx, responseCtx context.Context, body []byte, headers http.Header) (*http.Response, error) {
	service := g.openCode
	if service != nil {
		if service.Catalog != nil {
			// Refresh metadata only, never retry an inference request.
			_ = service.Catalog.Refresh(startCtx, false)
		}
		pinned := service.Pin()
		service = &pinned
	}
	tr, err := translateProviderRequest(body, service)
	if err != nil {
		return nil, err
	}
	label := "Grok"
	var credentials struct {
		endpoint string
		headers  http.Header
	}
	if g.openCode != nil {
		label = g.openCode.Label
		credentials.endpoint = g.openCode.Endpoint
		credentials.headers = http.Header{
			"Authorization": {"Bearer " + g.openCode.APIKey}, "User-Agent": {"mekugi"},
		}
		if session := openCodeSessionID(g.openCode.Prefix, headers.Get(threadIDHeader)); session != "" {
			credentials.headers.Set("x-opencode-session", session)
		}
		switch tr.format {
		case "anthropic":
			credentials.endpoint = strings.TrimSuffix(g.openCode.Endpoint, "/chat/completions") + "/messages"
			credentials.headers.Del("Authorization")
			credentials.headers.Set("x-api-key", g.openCode.APIKey)
			credentials.headers.Set("anthropic-version", "2023-06-01")
		case "responses":
			credentials.endpoint = strings.TrimSuffix(g.openCode.Endpoint, "/chat/completions") + "/responses"
		}
	} else {
		selected, authErr := g.auth.Credentials(startCtx)
		credentials.endpoint, credentials.headers, err = selected.Endpoint, selected.Headers, authErr
		if err != nil {
			return nil, err
		}
		if tr.body["model"] == "grok-4.7-build-fast" && g.auth.UsesAPIKey() {
			return nil, incompatibleRequest("grok_fast_requires_build", "Grok 4.7 Build Fast requires Grok OAuth; it is not available on the public xAI API.")
		}
		if !g.auth.UsesAPIKey() {
			credentials.headers.Set("X-Grok-Model-Override", tr.body["model"].(string))
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
	if response.StatusCode == http.StatusUnauthorized && g.openCode == nil && !g.auth.UsesAPIKey() {
		response.Body.Close()
		if err := g.auth.Refresh(startCtx, strings.TrimPrefix(credentials.headers.Get("Authorization"), "Bearer ")); err != nil {
			stopStart()
			cancel()
			return nil, err
		}
		selected, authErr := g.auth.Credentials(startCtx)
		credentials.endpoint, credentials.headers, err = selected.Endpoint, selected.Headers, authErr
		if err != nil {
			stopStart()
			cancel()
			return nil, err
		}
		credentials.headers.Set("X-Grok-Model-Override", tr.body["model"].(string))
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
			detail = []byte(fmt.Sprintf("provider error body could not be read: %v", readErr))
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
	var price *opencode.Price
	if service != nil {
		model, _ := tr.body["model"].(string)
		metadata, _ := service.Model(model)
		price = new(metadata.Cost)
	}
	failureDetail := func(body []byte) string {
		return newProviderHTTPError(label, 0, body, credentials.headers, headers).Error()
	}
	if !tr.stream {
		result, err := tr.codec.Read(upstream, func(map[string]any) error { return nil }, failureDetail)
		if err != nil {
			upstream.Close()
			return nil, err
		}
		data := responses.MustMarshalJSON(result)
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: &grokResponseBody{Reader: bytes.NewReader(data), close: upstream.Close, openCodePrice: price}}, nil
	}
	reader, writer := io.Pipe()
	stopCancel := context.AfterFunc(ctx, func() { writer.CloseWithError(ctx.Err()) })
	var streamErr error
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		_, err := tr.codec.Read(upstream, func(event map[string]any) error {
			_, err := fmt.Fprintf(writer, "event: %s\ndata: %s\n\n", event["type"], responses.MustMarshalJSON(event))
			return err
		}, failureDetail)
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
	openCodePrice *opencode.Price
	terminalError func() error
}

func (b *grokResponseBody) Close() error { return b.close() }

func isGrokModel(model string) bool {
	return strings.HasPrefix(model, "grok:") || strings.HasPrefix(model, "grok-")
}
