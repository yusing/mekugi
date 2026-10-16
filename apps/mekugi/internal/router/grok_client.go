package router

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
	upstream, err := httptransport.Exchange(startCtx, ctx, g.streamIdleTimeout, func(requestCtx context.Context) (*http.Response, error) {
		if service != nil {
			return service.Forward(requestCtx, g.httpClient, tr.format, headers.Get(codexresponses.ThreadIDHeader), encoded)
		}
		return grok.Forward(startCtx, requestCtx, g.httpClient, g.auth, tr.body["model"].(string), encoded)
	})
	if err != nil {
		cancel()
		if rejection, ok := errors.AsType[*httptransport.Rejection](err); ok {
			return nil, newProviderHTTPError(label, rejection.Status, rejection.Detail)
		}
		if _, ok := errors.AsType[*httptransport.SendError](err); ok {
			return nil, forwardCriticalDiagnostic(err)
		}
		if errors.Is(err, grok.ErrBuildFastRequiresOAuth) {
			return nil, incompatibleRequest("grok_fast_requires_build", err.Error())
		}
		return nil, err
	}
	response, err := tr.codec.Response(ctx, cancel, upstream.Body, tr.stream, func(body []byte) string {
		return newProviderHTTPError(label, 0, body).Error()
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
	response.Body = &grokResponseBody{ResponseBody: response.Body.(*codexresponses.ResponseBody), openCodePrice: price}
	return response, nil
}

type grokResponseBody struct {
	*codexresponses.ResponseBody
	openCodePrice *opencode.Price
}
