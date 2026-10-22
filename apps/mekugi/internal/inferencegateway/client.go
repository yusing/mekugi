package inferencegateway

// Source: apps/mekugi/internal/router/client.go:1:105@[bd606c09] provider routing.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/yusing/mekugi/providers/grok"
	"github.com/yusing/mekugi/providers/opencode"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/providers/openai"
)

const maxUpstreamErrorDetailBytes = 8 << 10

type Client struct {
	OpenAI         *openai.Client
	ThirdPartyOnly bool
	OpenCode       map[string]*ThirdPartyClient
	Grok           *ThirdPartyClient
	Diagnostics    codexresponses.Diagnostics
}

func NewClient(baseURL string, supplied *http.Client, diagnostics codexresponses.Diagnostics) *Client {
	provider := openai.New(baseURL, supplied)
	return &Client{OpenAI: provider, Diagnostics: diagnostics}
}

func (c *Client) ForwardModels(ctx context.Context, headers http.Header, rawQuery string) (*http.Response, error) {
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
	var model struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &model); err != nil {
		return nil, errors.New("invalid provider request")
	}
	if opencode.IsModel(model.Model) {
		prefix, _, _ := strings.Cut(model.Model, ":")
		client := c.OpenCode[prefix]
		if client == nil {
			return nil, IncompatibleRequest("opencode_not_configured", "OpenCode models require standalone mekugi with the provider's API key in the environment or Mekugi config.toml")
		}
		return client.ForwardExecution(startCtx, responseCtx, body, headers)
	}
	if grok.IsModel(model.Model) {
		if c.Grok == nil {
			return nil, IncompatibleRequest("grok_not_enabled", "Grok models require authenticated standalone mekugi or mekugi grok")
		}
		if c.Grok.Unprefixed != strings.HasPrefix(model.Model, "grok-") {
			return nil, IncompatibleRequest("grok_model_namespace", "Use plain Grok model IDs in mekugi grok, and grok: model IDs in standalone mekugi")
		}
		return c.Grok.ForwardExecution(startCtx, responseCtx, body, headers)
	}
	if c.ThirdPartyOnly {
		return nil, IncompatibleRequest("third_party_model_required", "This launch supports only its authenticated third-party models; use mekugi codex for OpenAI models")
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
	Price     *opencode.Price
	Transport string
}

// IsThirdPartyModel selects full-history routing, including Chat, Messages and Responses.
func IsThirdPartyModel(model string) bool { return grok.IsModel(model) || opencode.IsModel(model) }
