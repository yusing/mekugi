package openai

import (
	"context"
	"net/http"
	"strings"

	"github.com/coder/websocket"
)

// Dial opens one dedicated connection. On failed upgrade its original response
// body remains readable and belongs to the caller, including when err is non-nil.
func (c *Client) Dial(ctx context.Context, headers http.Header) (*websocket.Conn, *http.Response, error) {
	c.predictions.observe(headers)
	return dialWebSocket(ctx, c.httpClient, strings.TrimRight(c.baseURL, "/")+"/responses", headers)
}

func dialWebSocket(ctx context.Context, supplied *http.Client, endpoint string, headers http.Header) (*websocket.Conn, *http.Response, error) {
	client := *supplied
	client.Timeout = 0 // The caller owns setup and rejected-response deadlines.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	transport := &webSocketHandshakeTransport{RoundTripper: client.Transport}
	client.Transport = transport
	conn, response, err := websocket.Dial(ctx, endpoint, &websocket.DialOptions{HTTPClient: &client, HTTPHeader: headers})
	if transport.rejected != nil {
		response = transport.rejected
	}
	return conn, response, err
}

// Preserve non-upgrade HTTP bodies before the WebSocket library's bounded
// diagnostic read. Successful upgrades retain their io.ReadWriteCloser intact.
type webSocketHandshakeTransport struct {
	http.RoundTripper
	rejected *http.Response
}

func (transport *webSocketHandshakeTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := transport.RoundTripper.RoundTrip(request)
	if err != nil || response.StatusCode == http.StatusSwitchingProtocols {
		return response, err
	}
	transport.rejected = response
	headerOnly := *response
	headerOnly.Body = http.NoBody
	return &headerOnly, nil
}
