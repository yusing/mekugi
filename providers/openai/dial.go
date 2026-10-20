package openai

import (
	"context"
	"net/http"
	"strings"

	"github.com/coder/websocket"
)

// WebSocketRequest is prepared by the backend, independently of provider transport.
type WebSocketRequest struct {
	Payload []byte
	Headers http.Header
	Stream  bool
}

// Dial opens one dedicated connection. On failed upgrade its original response
// body remains readable and belongs to the caller, including when err is non-nil.
func (c *Client) Dial(ctx context.Context, headers http.Header) (*websocket.Conn, *http.Response, error) {
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
