package launch

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"

	responseevents "github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

func newTestProviderClient(baseURL string, client *http.Client) *inferencegateway.Client {
	return inferencegateway.NewClient(baseURL, client, requestexecution.ProviderDiagnostics())
}

func testGatewayResponse(response *http.Response, err error) (*inferencegateway.Response, error) {
	if response == nil {
		return nil, err
	}
	return &inferencegateway.Response{Response: response}, err
}

func testRequestExecutor(provider inferencegateway.Forwarder, issues *requestexecution.Failures, proxy *observationFixture) requestexecution.Executor {
	executor := requestexecution.Executor{Provider: provider, Issues: issues}
	if proxy != nil {
		executor.Observations, executor.Guidance = proxy, proxy.registry.Guidance
		executor.SideThreads, executor.Usage = proxy, proxy.usage
	}
	executor.Exchange, _ = provider.(*inferencegateway.Exchange)
	return executor
}

func testResponsesHandler(ctx context.Context, timeout time.Duration, provider inferencegateway.Forwarder, issues *requestexecution.Failures, proxy *observationFixture) http.HandlerFunc {
	return inferencegateway.ResponsesHandler(ctx, timeout, testRequestExecutor(provider, issues, proxy).ExecuteHTTP)
}

func testWebSocketEndpoint(ctx context.Context, timeout time.Duration, provider *inferencegateway.Client, issues *requestexecution.Failures, proxy *observationFixture) *inferencegateway.WebSocketEndpoint {
	endpoint := inferencegateway.NewWebSocketEndpoint(ctx, timeout, provider, testRequestExecutor(provider, issues, proxy).ExecuteWebSocket)
	if proxy != nil {
		endpoint.ForkCacheKey = proxy.ForkCacheKey
	}
	return endpoint
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

func customTestProvider(client *http.Client) *inferencegateway.Client {
	provider := newTestProviderClient("http://unused.invalid", nil)
	provider.Native = &inferencegateway.NativeProvider{ID: "custom", BaseURL: "http://custom.invalid/v1", HTTPClient: client, StreamIdleTimeout: time.Minute}
	return provider
}

func readResponseTestEvents(t *testing.T, source io.Reader, observe func(map[string]any) error) (responseevents.TerminalState, error) {
	t.Helper()
	response := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(source)}
	return requestexecution.CopyResponse(io.Discard, response, true, testProviderEvents(observe), nil)
}
