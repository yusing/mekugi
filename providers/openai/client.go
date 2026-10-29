package openai

// Source: internal/router/client.go:53:232,329:377@49f759d6 OpenAI credentials and exchange.
// Wire JSON retains the existing v1 acceptance and exact encoding semantics.
import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/yusing/mekugi/libraries/httptransport"
)

const (
	BaseURL         = "https://chatgpt.com/backend-api/codex"
	AccountIDHeader = "Chatgpt-Account-Id"
)

// Client owns provider connections, independently of backend conversation state.
// Configure StreamIdleTimeout before starting requests.
type Client struct {
	predictions       predictionSettingsCache
	httpClient        *http.Client
	baseURL           string
	StreamIdleTimeout time.Duration
}

func New(baseURL string, supplied *http.Client) *Client {
	return &Client{httpClient: httptransport.Client(supplied), baseURL: baseURL}
}

// Forward sends one auxiliary HTTP exchange without redirects or retries.
// The caller supplies an authorized path relative to the provider base URL.
func (c *Client) Forward(request *http.Request) (*http.Response, error) {
	upstream := request.Clone(request.Context())
	endpoint, err := url.Parse(strings.TrimRight(c.baseURL, "/") + request.URL.Path)
	if err != nil {
		return nil, err
	}
	endpoint.RawQuery = request.URL.RawQuery
	upstream.URL = endpoint
	client := *c.httpClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return client.Do(upstream)
}

// Credentials enforces the router's authentication boundary.
// Codex owns login and token refresh; a custom provider configured with
// requires_openai_auth = true attaches its managed credentials to every request.
// The router validates those headers and relays them to the ChatGPT Codex backend.
func Credentials(headers http.Header) (string, string, error) {
	authorizationValues := headers.Values("Authorization")
	if len(authorizationValues) != 1 {
		return "", "", errors.New("codex request must provide exactly one Authorization header; configure the provider with requires_openai_auth = true")
	}
	scheme, token, ok := strings.Cut(authorizationValues[0], " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" || token != strings.TrimSpace(token) {
		return "", "", errors.New("codex request has an invalid bearer Authorization header")
	}
	accountValues := headers.Values(AccountIDHeader)
	if len(accountValues) != 1 || accountValues[0] == "" || accountValues[0] != strings.TrimSpace(accountValues[0]) {
		return "", "", errors.New("codex request must provide exactly one non-empty ChatGPT-Account-ID header")
	}
	return authorizationValues[0], accountValues[0], nil
}

func (c *Client) Models(ctx context.Context, headers http.Header, rawQuery string) (*http.Response, error) {
	c.predictions.observe(headers)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.baseURL, "/")+"/models", nil)
	if err != nil {
		return nil, fmt.Errorf("build upstream models request: %w", err)
	}
	request.URL.RawQuery = rawQuery
	request.Header = headers.Clone()
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("forward upstream models request: %w", err)
	}
	return response, nil
}

// Responses forwards one prepared HTTP attempt. Codex owns retries and fallback.
func (c *Client) Responses(startCtx, responseCtx context.Context, body []byte, headers http.Header) (*http.Response, error) {
	c.predictions.observe(headers)
	requestCtx, cancelRequest := context.WithCancel(responseCtx)
	stopStartCancellation := context.AfterFunc(startCtx, cancelRequest)
	endpoint := strings.TrimRight(c.baseURL, "/") + "/responses"
	request, err := httptransport.NewRequest(requestCtx, http.MethodPost, endpoint, body)
	if err != nil {
		stopStartCancellation()
		cancelRequest()
		return nil, fmt.Errorf("build upstream request: %w", err)
	}
	request.Header = headers.Clone()
	client := *c.httpClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		stopStartCancellation()
		cancelRequest()
		return nil, fmt.Errorf("forward upstream request: %w", err)
	}
	if !stopStartCancellation() {
		cancelRequest()
		_ = response.Body.Close()
		if err := startCtx.Err(); err != nil {
			return nil, fmt.Errorf("forward upstream request: %w", err)
		}
		return nil, fmt.Errorf("forward upstream request: %w", context.Canceled)
	}
	var result io.ReadCloser = httptransport.CancelOnClose(response.Body, cancelRequest)
	if c.StreamIdleTimeout > 0 {
		result = httptransport.IdleRead(responseCtx, result, c.StreamIdleTimeout)
	}
	response.Body = result
	return response, nil
}
