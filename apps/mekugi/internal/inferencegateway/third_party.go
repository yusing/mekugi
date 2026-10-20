package inferencegateway

// Source: apps/mekugi/internal/router/grok_client.go:1:89@[bd606c09] third-party transport.

import (
	"context"
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"errors"
	"net/http"
	"time"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/httptransport"
	"github.com/yusing/mekugi/providers/grok"
	"github.com/yusing/mekugi/providers/opencode"
)

type ThirdPartyClient struct {
	HTTPClient        *http.Client
	OpenCode          *opencode.Service
	Auth              *grok.Auth
	Unprefixed        bool
	StreamIdleTimeout time.Duration
	Diagnostics       codexresponses.Diagnostics
}

func (g *ThirdPartyClient) ForwardExecution(startCtx, responseCtx context.Context, body []byte, headers http.Header) (*Response, error) {
	service := g.OpenCode
	if service != nil {
		if service.Catalog != nil {
			// Refresh metadata only, never retry an inference request.
			_ = service.Catalog.Refresh(startCtx, false)
		}
		pinned := service.Pin()
		service = &pinned
	}
	tr, err := translateProviderRequest(body, service, g.Diagnostics)
	if err != nil {
		return nil, err
	}
	// Preserve existing provider wire bytes and permissive raw-JSON semantics.
	encoded, err := json.Marshal(&tr.body, jsonv1.DefaultOptionsV1())
	if err != nil {
		return nil, err
	}
	label := "Grok"
	if service != nil {
		label = service.Label
	}
	// The bridge owns this ancestor so streaming close can cancel before closing
	// the captured source. Exchange independently owns HTTP admission and release.
	ctx, cancel := context.WithCancel(responseCtx)
	upstream, err := httptransport.Exchange(startCtx, ctx, g.StreamIdleTimeout, func(requestCtx context.Context) (*http.Response, error) {
		if service != nil {
			return service.Forward(requestCtx, g.HTTPClient, tr.format, headers.Get(codexresponses.ThreadIDHeader), encoded)
		}
		return grok.Forward(startCtx, requestCtx, g.HTTPClient, g.Auth, tr.body["model"].(string), encoded)
	})
	if err != nil {
		cancel()
		if rejection, ok := errors.AsType[*httptransport.Rejection](err); ok {
			return nil, NewProviderHTTPError(label, rejection.Status, rejection.Detail)
		}
		if _, ok := errors.AsType[*httptransport.SendError](err); ok {
			return nil, g.Diagnostics.ForwardError(err)
		}
		if errors.Is(err, grok.ErrBuildFastRequiresOAuth) {
			return nil, IncompatibleRequest("grok_fast_requires_build", err.Error())
		}
		return nil, err
	}
	response, err := tr.codec.Response(ctx, cancel, upstream.Body, tr.stream, func(body []byte) string {
		return NewProviderHTTPError(label, 0, body).Error()
	})
	if err != nil {
		return nil, err
	}
	var price *opencode.Price
	if service != nil {
		model, _ := tr.body["model"].(string)
		metadata, _ := service.Model(model)
		price = new(metadata.Cost)
	}
	return &Response{Response: response, Price: price, Transport: "chat_response_bridge"}, nil
}
