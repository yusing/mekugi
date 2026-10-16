package router

// Source: client.go:1:679 upstream forwarding, response transformation, and usage observation.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/yusing/mekugi/providers/grok"
	"github.com/yusing/mekugi/providers/opencode"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"
	"github.com/yusing/mekugi/providers/openai"
)

const maxUpstreamErrorDetailBytes = 8 << 10

type providerClient struct {
	openai         *openai.Client
	thirdPartyOnly bool
	titleGenerator *sessionNaming
	serviceTiers   *serviceTierSettings
	opencode       map[string]*grokClient
	grok           *grokClient
}

func newProviderClient(baseURL string, supplied *http.Client) *providerClient {
	provider := openai.New(baseURL, supplied)
	provider.ObserveWebSocket = func(ctx context.Context, payload []byte, headers, responseHeaders http.Header) openai.Observation {
		return transportcapture.BeginWebSocketAttempt(ctx, payload, headers, responseHeaders)
	}
	return &providerClient{openai: provider}
}

func (c *providerClient) forwardModels(ctx context.Context, headers http.Header, rawQuery string) (*http.Response, error) {
	authorization, account, err := openai.Credentials(headers)
	if err != nil {
		return nil, criticalDiagnostic(err, "models_auth_headers", "the model catalog request is missing valid Codex Authorization or account headers", true)
	}
	upstream := http.Header{}
	upstream.Set("Authorization", authorization)
	upstream.Set(openai.AccountIDHeader, account)
	upstream.Set("Accept", "application/json")
	upstream.Set("Originator", codexresponses.ClientIdentity)
	upstream.Set("User-Agent", codexresponses.ClientIdentity)
	return c.openai.Models(ctx, upstream, rawQuery)
}

func (c *providerClient) forwardExecution(startCtx, responseCtx context.Context, body []byte, headers http.Header, cacheKey string) (*http.Response, error) {
	var model struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &model); err != nil {
		return nil, errors.New("invalid provider request")
	}
	if opencode.IsModel(model.Model) {
		prefix, _, _ := strings.Cut(model.Model, ":")
		client := c.opencode[prefix]
		if client == nil {
			return nil, incompatibleRequest("opencode_not_configured", "OpenCode models require standalone mekugi with the provider's API key in the environment or Mekugi config.toml")
		}
		return client.forwardExecution(startCtx, responseCtx, body, headers)
	}
	if grok.IsModel(model.Model) {
		if c.grok == nil {
			return nil, incompatibleRequest("grok_not_enabled", "Grok models require authenticated standalone mekugi or mekugi grok")
		}
		if c.grok.unprefixed != strings.HasPrefix(model.Model, "grok-") {
			return nil, incompatibleRequest("grok_model_namespace", "Use plain Grok model IDs in mekugi grok, and grok: model IDs in standalone mekugi")
		}
		return c.grok.forwardExecution(startCtx, responseCtx, body, headers)
	}
	if c.thirdPartyOnly {
		return nil, incompatibleRequest("third_party_model_required", "This launch supports only its authenticated third-party models; use mekugi codex for OpenAI models")
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
	upstream.Set("Originator", codexresponses.ClientIdentity)
	upstream.Set("User-Agent", codexresponses.ClientIdentity)
	codexresponses.ForwardHeaders(upstream, headers)
	if codexresponses.ValidCacheKey(cacheKey) {
		upstream[codexresponses.SessionIDHeader] = []string{cacheKey}
	}
	var websocket *openai.WebSocketRequest
	if c.openai.WebSocketsEnabled() {
		payload, handshake, stream, err := codexresponses.WebSocketRequest(body, upstream, cacheKey)
		if err != nil {
			return nil, err
		}
		websocket = &openai.WebSocketRequest{Payload: payload, Headers: handshake, Stream: stream}
	}
	return c.openai.Responses(startCtx, responseCtx, body, upstream, websocket)
}
