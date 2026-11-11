package inferencegateway

// Source: apps/mekugi/internal/router/client.go:1:105@[bd606c09] provider routing.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/yusing/mekugi/libraries/httptransport"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/providers/openai"
)

const maxUpstreamErrorDetailBytes = 8 << 10

type Client struct {
	OpenAI      *openai.Client
	Diagnostics codexresponses.Diagnostics
	Native      *NativeProvider
}

func NewClient(baseURL string, supplied *http.Client, diagnostics codexresponses.Diagnostics) *Client {
	provider := openai.New(baseURL, supplied)
	return &Client{OpenAI: provider, Diagnostics: diagnostics}
}

func (c *Client) ForwardModels(ctx context.Context, headers http.Header, rawQuery string) (*http.Response, error) {
	if c.Native != nil {
		return c.Native.models(ctx, headers, rawQuery)
	}
	authorization, account, err := openai.Credentials(headers)
	if err != nil {
		return nil, &ModelCredentialsError{err: err}
	}
	upstream := http.Header{}
	upstream.Set("Authorization", authorization)
	upstream.Set(openai.AccountIDHeader, account)
	upstream.Set("Accept", "application/json")
	codexresponses.ForwardIdentityHeaders(upstream, headers)
	return c.OpenAI.Models(ctx, upstream, rawQuery)
}

// ModelCredentialsError distinguishes local catalog admission from provider failure.
type ModelCredentialsError struct{ err error }

func (e *ModelCredentialsError) Error() string { return e.err.Error() }
func (e *ModelCredentialsError) Unwrap() error { return e.err }

func (c *Client) ForwardExecution(startCtx, responseCtx context.Context, body []byte, headers http.Header, cacheKey string) (*Response, error) {
	if c.Native != nil {
		response, err := c.Native.forward(startCtx, responseCtx, body, headers)
		if rejection, ok := errors.AsType[*httptransport.Rejection](err); ok {
			if _, limited, _ := ProviderWait(rejection.Detail, rejection.Header, time.Now()); limited {
				return &Response{Response: &http.Response{StatusCode: rejection.Status, Header: rejection.Header, Body: io.NopCloser(bytes.NewReader(rejection.Detail))}, Provider: c.Native.ID, UnknownPrice: true}, nil
			}
			failure := NewProviderHTTPError(c.Native.ID, rejection.Status, rejection.Detail)
			failure.Header = rejection.Header
			return nil, failure
		}
		return response, err
	}
	authorization, account, err := openai.Credentials(headers)
	if err != nil {
		return nil, err
	}
	upstream := http.Header{}
	upstream.Set("Authorization", authorization)
	upstream.Set(openai.AccountIDHeader, account)
	upstream.Set("Content-Type", "application/json")
	upstream.Set("Accept", "application/json, text/event-stream")
	codexresponses.ForwardHeaders(upstream, headers)
	if codexresponses.ValidCacheKey(cacheKey) {
		upstream[codexresponses.SessionIDHeader] = []string{cacheKey}
	}
	response, err := c.OpenAI.Responses(startCtx, responseCtx, body, upstream)
	if err != nil {
		return nil, err
	}
	return &Response{Response: response, Transport: "http"}, nil
}

// Forwarder executes a prepared request without owning request preparation.
type Forwarder interface {
	ForwardExecution(context.Context, context.Context, []byte, http.Header, string) (*Response, error)
}

// Response carries exchange facts separately from its consumable body.
type Response struct {
	*http.Response
	Transport    string
	UnknownPrice bool
	Provider     string
}
