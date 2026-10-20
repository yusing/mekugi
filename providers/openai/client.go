package openai

// Source: internal/router/client.go:53:232,329:377@49f759d6 OpenAI credentials and exchange.
// Wire JSON retains the existing v1 acceptance and exact encoding semantics.
import (
	"bytes"
	"context"
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/yusing/mekugi/libraries/httptransport"
)

const (
	BaseURL                     = "https://chatgpt.com/backend-api/codex"
	AccountIDHeader             = "Chatgpt-Account-Id"
	maxUpstreamErrorDetailBytes = 8 << 10
	maxUpstreamSniffBytes       = 64 << 10
	upstreamJSONBufferBytes     = 64 << 20
	maxUpstreamRetries          = 5
	initialUpstreamRetryBackoff = 100 * time.Millisecond
	maxUpstreamRetryBackoff     = 30 * time.Second
	selectedModelAtCapacity     = "selected model is at capacity"
)

// Client owns provider connections, independently of backend conversation state.
// Configure StreamIdleTimeout and ObserveWebSocket before starting requests.
type Client struct {
	httpClient        *http.Client
	baseURL           string
	websockets        *providerWebSockets
	StreamIdleTimeout time.Duration
	ObserveWebSocket  func(context.Context, []byte, http.Header, http.Header) Observation
}

func New(baseURL string, supplied *http.Client) *Client {
	return &Client{httpClient: httptransport.Client(supplied), baseURL: baseURL}
}

// WebSocketsEnabled reports whether full-input requests may use the pool.
func (c *Client) WebSocketsEnabled() bool { return c.websockets != nil }

// Close releases pooled resources; dedicated sockets belong to their caller.
func (c *Client) Close() {
	if c.websockets != nil {
		c.websockets.close()
	}
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

// Responses sends the prepared HTTP request, or its prepared WebSocket equivalent.
// A nil websocket request selects HTTP without opening a pool connection.
func (c *Client) Responses(startCtx, responseCtx context.Context, body []byte, headers http.Header, websocket *WebSocketRequest) (*http.Response, error) {
	if c.websockets != nil && websocket != nil {
		response, fallback, err := c.forwardWebSocket(startCtx, responseCtx, websocket, headers)
		if !fallback {
			return response, err
		}
	}
	endpoint := strings.TrimRight(c.baseURL, "/") + "/responses"
	for attempt := 0; ; attempt++ {
		requestCtx, cancelRequest := context.WithCancel(responseCtx)
		stopStartCancellation := context.AfterFunc(startCtx, cancelRequest)
		request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			stopStartCancellation()
			cancelRequest()
			return nil, fmt.Errorf("build upstream request: %w", err)
		}
		request.Header = headers.Clone()
		response, err := c.httpClient.Do(request)
		if err != nil {
			stopStartCancellation()
			cancelRequest()
			return nil, fmt.Errorf("forward upstream request: %w", err)
		}
		finishResponseStart := func() error {
			if !stopStartCancellation() {
				cancelRequest()
				_ = response.Body.Close()
				if err := startCtx.Err(); err != nil {
					return err
				}
				return context.Canceled
			}
			var body io.ReadCloser = httptransport.CancelOnClose(response.Body, cancelRequest)
			if c.StreamIdleTimeout > 0 {
				body = httptransport.IdleRead(responseCtx, body, c.StreamIdleTimeout)
			}
			response.Body = body
			return nil
		}
		recoverable := responseHasRecoverableUpstreamError(response)
		if err := finishResponseStart(); err != nil {
			return nil, fmt.Errorf("forward upstream request: %w", err)
		}
		if !recoverable || attempt == maxUpstreamRetries {
			return response, nil
		}
		delay := upstreamRetryDelay(response, attempt)
		_ = response.Body.Close()
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-startCtx.Done():
			timer.Stop()
			return nil, fmt.Errorf("wait to retry upstream request: %w", startCtx.Err())
		}
	}
}

func upstreamRetryDelay(response *http.Response, attempt int) time.Duration {
	if value := strings.TrimSpace(response.Header.Get("Retry-After")); value != "" {
		if seconds, err := strconv.ParseUint(value, 10, 64); err == nil {
			if seconds >= uint64(maxUpstreamRetryBackoff/time.Second) {
				return maxUpstreamRetryBackoff
			}
			return min(time.Duration(seconds)*time.Second, maxUpstreamRetryBackoff)
		}
		if retryAt, err := http.ParseTime(value); err == nil {
			return min(max(time.Until(retryAt), 0), maxUpstreamRetryBackoff)
		}
	}
	delay := min(initialUpstreamRetryBackoff<<attempt, maxUpstreamRetryBackoff)
	half := delay / 2
	return half + rand.N(delay-half)
}

func responseHasRecoverableUpstreamError(response *http.Response) bool {
	if response == nil || response.Body == nil || response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices {
		return false
	}
	detail, err := io.ReadAll(io.LimitReader(response.Body, maxUpstreamErrorDetailBytes+1))
	httptransport.RestorePrefix(response, detail, response.Body)
	if err != nil || len(detail) > maxUpstreamErrorDetailBytes {
		return false
	}
	message := strings.ToLower(upstreamErrorMessage(detail))
	return strings.Contains(message, selectedModelAtCapacity)
}

func upstreamErrorMessage(body []byte) string {
	var envelope struct {
		Error jsontext.Value `json:"error"`
	}
	if json.Unmarshal(body, &envelope, jsonv1.DefaultOptionsV1()) != nil || len(envelope.Error) == 0 {
		return ""
	}
	var providerError struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(envelope.Error, &providerError, jsonv1.DefaultOptionsV1()) == nil {
		return strings.TrimSpace(providerError.Message)
	}
	var message string
	if json.Unmarshal(envelope.Error, &message, jsonv1.DefaultOptionsV1()) == nil {
		return strings.TrimSpace(message)
	}
	return ""
}
