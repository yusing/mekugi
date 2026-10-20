package httptransport

// Source: internal/router/client.go:780:794@49f759d6 HTTP client defaults.
import (
	"net"
	"net/http"
	"time"
)

// Client clones client, supplying dial defaults only when its transport is nil.
func Client(client *http.Client) *http.Client {
	if client == nil {
		client = &http.Client{}
	}
	cloned := *client
	if cloned.Transport == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.DialContext = (&net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext
		cloned.Transport = transport
	}
	return &cloned
}
