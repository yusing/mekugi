package router

import (
	"context"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"

	"github.com/yusing/mekugi/providers/grok"
)

func TestGrokOAuthRetryRetainsTransportCause(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		name := "transport"
		if canceled {
			name = "cancellation"
		}
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "auth.json")
			grokWriteTestAuth(t, path, time.Now().Add(time.Hour))
			auth := grok.NewAuth(path, "", &http.Client{Transport: grokTestTransport(func(r *http.Request) (*http.Response, error) {
				if r.Method == http.MethodGet {
					return serverHTTPResponse(`{"issuer":"https://auth.x.ai","token_endpoint":"https://auth.x.ai/token"}`), nil
				}
				return serverHTTPResponse(`{"access_token":"new-token","refresh_token":"new-refresh","expires_in":3600}`), nil
			})})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			cause := io.ErrUnexpectedEOF
			if canceled {
				cause = context.Canceled
			}
			calls := 0
			client := &inferencegateway.ThirdPartyClient{Diagnostics: providerCodecDiagnostics(), Auth: auth, HTTPClient: &http.Client{Transport: grokTestTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					response := serverHTTPResponse("rejected token")
					response.StatusCode = http.StatusUnauthorized
					return response, nil
				}
				if r.Header.Get("Authorization") != "Bearer new-token" {
					t.Error("retry did not use refreshed credentials")
				}
				if canceled {
					cancel()
					<-r.Context().Done()
					return nil, r.Context().Err()
				}
				return nil, cause
			})}}
			response, err := client.ForwardExecution(ctx, ctx, grokTestRequest(t, true), grokTestHeaders())
			if response != nil {
				response.Body.Close()
				t.Fatal("failed retry returned a response")
			}
			if calls != 2 || !errors.Is(err, cause) {
				t.Fatalf("calls=%d retry error=%v, want retained cause %v", calls, err, cause)
			}
		})
	}
}
