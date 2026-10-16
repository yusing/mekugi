package grok

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/yusing/mekugi/libraries/httptransport"
)

// Source: internal/router/grok_client.go:64:132@8cd86549 authenticated inference.

var ErrBuildFastRequiresOAuth = errors.New("Grok 4.7 Build Fast requires Grok OAuth; it is not available on the public xAI API.")

// Forward sends an encoded inference request and retries one rejected OAuth
// token. The final response belongs to the caller; no Codex headers are accepted.
func Forward(startCtx, requestCtx context.Context, supplied *http.Client, auth *Auth, model string, body []byte) (*http.Response, error) {
	credentials, err := auth.Credentials(startCtx)
	if err != nil {
		return nil, err
	}
	if model == "grok-4.7-build-fast" && auth.UsesAPIKey() {
		return nil, ErrBuildFastRequiresOAuth
	}
	client := httptransport.Client(supplied)
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	endpoint := credentials.Endpoint
	send := func(prefix string) (*http.Response, error) {
		request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, errors.New("invalid Grok endpoint")
		}
		request.Header = credentials.Headers.Clone()
		if !auth.UsesAPIKey() {
			request.Header.Set("X-Grok-Model-Override", model)
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "text/event-stream")
		response, err := client.Do(request)
		if err != nil {
			return nil, &httptransport.SendError{Err: fmt.Errorf("%s: %w", prefix, err)}
		}
		return response, nil
	}
	response, err := send("Grok request failed")
	if err != nil || response.StatusCode != http.StatusUnauthorized || auth.UsesAPIKey() {
		return response, err
	}
	response.Body.Close()
	if err := auth.Refresh(startCtx, strings.TrimPrefix(credentials.Headers.Get("Authorization"), "Bearer ")); err != nil {
		return nil, err
	}
	credentials, err = auth.Credentials(startCtx)
	if err != nil {
		return nil, err
	}
	return send("grok retry after authentication refresh failed")
}
