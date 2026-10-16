package router

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/libraries/httptransport"
	responseevents "github.com/yusing/mekugi/libraries/modelprotocol/responses"
	"github.com/yusing/mekugi/providers/grok"
)

func newTestProviderClient(baseURL string, client *http.Client) *inferencegateway.Client {
	return inferencegateway.NewClient(baseURL, client, providerCodecDiagnostics())
}

func testGatewayResponse(response *http.Response, err error) (*inferencegateway.Response, error) {
	if response == nil {
		return nil, err
	}
	return &inferencegateway.Response{Response: response}, err
}

func testRequestExecutor(provider inferencegateway.Forwarder, issues *CriticalErrors, proxy *mekugiProxy) requestExecutor {
	executor := requestExecutor{provider: provider, issues: issues, mekugiCalls: proxy}
	executor.client, _ = provider.(*inferencegateway.Client)
	executor.exchange, _ = provider.(*inferencegateway.Exchange)
	return executor
}

func testResponsesHandler(ctx context.Context, timeout time.Duration, provider inferencegateway.Forwarder, issues *CriticalErrors, proxy *mekugiProxy) http.HandlerFunc {
	return responsesHandler(ctx, timeout, testRequestExecutor(provider, issues, proxy))
}

func testWebSocketEndpoint(ctx context.Context, timeout time.Duration, provider *inferencegateway.Client, issues *CriticalErrors, proxy *mekugiProxy) *inferencegateway.WebSocketEndpoint {
	return inferencegateway.NewWebSocketEndpoint(ctx, timeout, provider, testRequestExecutor(provider, issues, proxy).executeWebSocket)
}

// Exercise application consumers through the same translated HTTP response that
// the production gateway supplies, rather than reaching into its codec policy.
func readGrokTestEvents(t *testing.T, request []byte, source io.Reader, observe func(map[string]any) error) (responseevents.TerminalState, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "auth.json")
	grokWriteTestAuth(t, path, time.Now().Add(time.Hour))
	client := &inferencegateway.ThirdPartyClient{
		Auth: grok.NewAuth(path, "", httptransport.Client(nil)), Diagnostics: providerCodecDiagnostics(),
		HTTPClient: &http.Client{Transport: grokTestTransport(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(source)}, nil
		})},
	}
	response, err := client.ForwardExecution(t.Context(), t.Context(), request, grokTestHeaders())
	if err != nil {
		return responseevents.TerminalUnknown, err
	}
	return copyUpstreamBodyTransformed(io.Discard, response.Response, true, testProviderEvents(observe), nil)
}

type testProviderEvents func(map[string]any) error

func (f testProviderEvents) TransformJSON(body []byte) ([]byte, error) { return body, nil }
func (f testProviderEvents) Finish(bool) error                         { return nil }
func (f testProviderEvents) TransformSSE(body []byte) ([][]byte, error) {
	var event map[string]any
	if err := json.Unmarshal(body, &event); err != nil {
		return nil, err
	}
	if err := f(event); err != nil {
		return nil, err
	}
	return [][]byte{body}, nil
}
