package inferencegateway

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/httptransport"
	"github.com/yusing/mekugi/providers/openai"
)

// NativeProvider pins a host-selected Responses provider. Authentication and
// retries remain with Codex; this owner never looks up credentials or models.
type NativeProvider struct {
	ID, BaseURL, CatalogURL string
	HTTPClient              *http.Client
	WebSockets              bool
	ConnectTimeout          time.Duration
	StreamIdleTimeout       time.Duration
	HeaderNames             []string
}

type queryContextKey struct{}

// WithProviderQuery carries the host's exact query without re-encoding it.
func WithProviderQuery(ctx context.Context, query string) context.Context {
	return context.WithValue(ctx, queryContextKey{}, query)
}

func ProviderQuery(ctx context.Context) string {
	query, _ := ctx.Value(queryContextKey{}).(string)
	return query
}

// AuxiliaryHeaders captures only the selected provider's authentication and
// configured headers, never the triggering exchange's execution identity.
func AuxiliaryHeaders(provider Forwarder, headers http.Header) (http.Header, error) {
	if selected, ok := provider.(interface {
		AuxiliaryHeaders(http.Header) (http.Header, error)
	}); ok {
		return selected.AuxiliaryHeaders(headers)
	}
	return openAIAuxiliaryHeaders(headers)
}

func (c *Client) AuxiliaryHeaders(headers http.Header) (http.Header, error) {
	if c.Native == nil {
		return openAIAuxiliaryHeaders(headers)
	}
	allowed := NativeHeaders(headers)
	out := make(http.Header)
	for _, name := range append([]string{"Authorization", openai.AccountIDHeader}, c.Native.HeaderNames...) {
		out[http.CanonicalHeaderKey(name)] = append([]string(nil), allowed.Values(name)...)
	}
	identity := make(http.Header)
	codexresponses.ForwardHeaders(identity, headers)
	for name := range identity {
		out.Del(name)
	}
	out.Del(codexresponses.SessionIDHeader)
	out.Del(codexresponses.RoutingSessionHeader)
	return out, nil
}

func openAIAuxiliaryHeaders(headers http.Header) (http.Header, error) {
	authorization, account, err := openai.Credentials(headers)
	if err != nil {
		return nil, err
	}
	return http.Header{"Authorization": {authorization}, openai.AccountIDHeader: {account}}, nil
}

func (p *NativeProvider) endpoint(path, query string) (string, error) {
	base, err := url.Parse(p.BaseURL)
	if err != nil {
		return "", fmt.Errorf("invalid pinned provider endpoint")
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/" + path
	if base.RawPath != "" {
		base.RawPath = strings.TrimRight(base.RawPath, "/") + "/" + path
	}
	if query != "" {
		if base.RawQuery != "" {
			base.RawQuery += "&"
		}
		base.RawQuery += query
	}
	return base.String(), nil
}

// NativeHeaders preserves host-supplied provider/auth headers but never local
// control or hop-by-hop state. Connection-nominated headers are removed too.
func NativeHeaders(headers http.Header) http.Header {
	out := make(http.Header)
	copyAuxiliaryResponseHeaders(out, headers)
	for name := range out {
		lower := strings.ToLower(name)
		if strings.HasPrefix(lower, "x-mekugi-") || strings.HasPrefix(lower, "sec-websocket-") || lower == "host" || lower == "content-length" {
			out.Del(name)
		}
	}
	return out
}

func (p *NativeProvider) forward(startCtx, responseCtx context.Context, body []byte, headers http.Header) (*Response, error) {
	query, _ := startCtx.Value(queryContextKey{}).(string)
	endpoint, err := p.endpoint("responses", query)
	if err != nil {
		return nil, err
	}
	response, err := httptransport.Exchange(startCtx, responseCtx, p.StreamIdleTimeout, func(ctx context.Context) (*http.Response, error) {
		request, err := httptransport.NewRequest(ctx, http.MethodPost, endpoint, body)
		if err != nil {
			return nil, err
		}
		request.Header = NativeHeaders(headers)
		// HTTP admission has already decoded and projected the JSON body.
		request.Header.Del("Content-Encoding")
		request.Header.Set("Content-Type", "application/json")
		return p.send(request)
	})
	if err != nil {
		return nil, err
	}
	filtered := make(http.Header)
	copyAuxiliaryResponseHeaders(filtered, response.Header)
	response.Header = filtered
	return &Response{Response: response, Transport: "http", UnknownPrice: true, Provider: p.ID}, nil
}

func (p *NativeProvider) send(request *http.Request) (*http.Response, error) {
	client := *p.HTTPClient
	client.Timeout = 0 // Contexts and idle reads own the operation's bounds.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return client.Do(request)
}

func (p *NativeProvider) models(ctx context.Context, headers http.Header, query string) (*http.Response, error) {
	if p.CatalogURL == "" {
		return nil, IncompatibleRequest("provider_catalog_unavailable", "the selected provider has no remote model catalog")
	}
	endpoint, err := url.Parse(p.CatalogURL)
	if err != nil {
		return nil, err
	}
	// The original catalog URL is complete. Native runtime-added query values
	// (client version, etc.) are appended without decoding/re-encoding either.
	if query != "" {
		if endpoint.RawQuery != "" {
			endpoint.RawQuery += "&"
		}
		endpoint.RawQuery += query
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, err
	}
	request.Header = NativeHeaders(headers)
	return p.send(request)
}

// Dial uses the same pinned route for the dedicated connection, including failed
// upgrade bodies, which remain readable for the existing bounded error policy.
func (c *Client) Dial(ctx context.Context, headers http.Header) (*websocket.Conn, *http.Response, error) {
	if c.Native == nil {
		return c.OpenAI.Dial(ctx, headers)
	}
	p := c.Native
	if !p.WebSockets {
		return nil, nil, IncompatibleRequest("provider_websocket_unavailable", "the selected provider does not support WebSockets")
	}
	if p.ConnectTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, p.ConnectTimeout)
		defer cancel()
	}
	query, _ := ctx.Value(queryContextKey{}).(string)
	endpoint, err := p.endpoint("responses", query)
	if err != nil {
		return nil, nil, err
	}
	client := *p.HTTPClient
	client.Timeout = 0
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	transport := &nativeHandshakeTransport{RoundTripper: client.Transport}
	client.Transport = transport
	conn, response, err := websocket.Dial(ctx, endpoint, &websocket.DialOptions{HTTPClient: &client, HTTPHeader: NativeHeaders(headers)})
	if transport.rejected != nil {
		response = transport.rejected
	}
	return conn, response, err
}

type nativeHandshakeTransport struct {
	http.RoundTripper
	rejected *http.Response
}

func (t *nativeHandshakeTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := t.RoundTripper.RoundTrip(request)
	if err != nil || response.StatusCode == http.StatusSwitchingProtocols {
		return response, err
	}
	t.rejected = response
	clone := *response
	clone.Body = http.NoBody
	return &clone, nil
}

func (c *Client) streamIdleTimeout() time.Duration {
	if c.Native != nil {
		return c.Native.StreamIdleTimeout
	}
	return c.OpenAI.StreamIdleTimeout
}

func (c *Client) webSocketRequest(body []byte, headers http.Header, cacheKey string) ([]byte, http.Header, error) {
	if c.Native == nil {
		return openAIWebSocketRequest(body, headers, cacheKey)
	}
	payload, handshake, _, err := codexresponses.WebSocketRequest(body, NativeHeaders(headers), cacheKey)
	handshake.Del("Content-Encoding")
	if value := headers.Get("OpenAI-Beta"); value != "" {
		handshake.Set("OpenAI-Beta", value)
	}
	return payload, handshake, err
}

func (c *Client) NativeContinuations() bool {
	if c.Native != nil {
		return c.Native.WebSockets
	}
	return true
}

func (c *Client) ApplyOpenAIPolicy() bool {
	return c.Native == nil
}

func (e *Exchange) NativeContinuations() bool {
	return e.session.provider.NativeContinuations()
}
func (e *Exchange) ApplyOpenAIPolicy() bool {
	return e.session.provider.ApplyOpenAIPolicy()
}

// NativeContinuations and ApplyOpenAIPolicy are independent: a custom native
// Responses connection has ancestry without acquiring ChatGPT-only policy.
func NativeContinuations(provider Forwarder) bool {
	if selected, ok := provider.(interface{ NativeContinuations() bool }); ok {
		return selected.NativeContinuations()
	}
	return true
}

func ApplyOpenAIPolicy(provider Forwarder) bool {
	if selected, ok := provider.(interface{ ApplyOpenAIPolicy() bool }); ok {
		return selected.ApplyOpenAIPolicy()
	}
	return true
}
