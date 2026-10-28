package connector

import (
	"context"
	"crypto/subtle"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
)

func (c *Connector) resource(w http.ResponseWriter, r *http.Request) {
	if origin := r.Header.Get("Origin"); origin != "" {
		w.Header().Set("Access-Control-Allow-Origin", origin)
	}
	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Methods", "POST")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, "+sessionapi.ProtocolHeader)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	c.mu.Lock()
	credentials := r.Header.Values("Authorization")
	valid := len(credentials) == 1 && strings.HasPrefix(credentials[0], "Bearer ") && subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(credentials[0], "Bearer ")), []byte(c.token)) == 1
	if !valid {
		c.mu.Unlock()
		http.Error(w, "A valid connector token is required.", http.StatusUnauthorized)
		return
	}
	if c.closed || c.connections >= connectionLimit {
		c.mu.Unlock()
		http.Error(w, "The connector cannot accept another request now.", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithCancel(c.generation)
	c.connections++
	c.relays.Add(1)
	c.mu.Unlock()
	defer func() { cancel(); c.mu.Lock(); c.connections--; c.mu.Unlock(); c.relays.Done() }()
	stop := context.AfterFunc(r.Context(), cancel)
	defer stop()
	stopWrite := context.AfterFunc(ctx, func() {
		controller := http.NewResponseController(w)
		_ = controller.SetReadDeadline(time.Now())
		_ = controller.SetWriteDeadline(time.Now())
	})
	defer stopWrite()
	// Construct the request from owned facts. Credentials and authority headers
	// never reach the private socket, and redirects never forward this request.
	upstream, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://session"+r.URL.Path, http.MaxBytesReader(w, r.Body, 128<<10))
	if err != nil {
		http.Error(w, "Invalid resource request.", http.StatusBadRequest)
		return
	}
	upstream.Header.Set("Content-Type", "application/json")
	upstream.Header[sessionapi.ProtocolHeader] = r.Header.Values(sessionapi.ProtocolHeader)
	response, err := c.transport.RoundTrip(upstream)
	if err != nil {
		if _, oversized := errors.AsType[*http.MaxBytesError](err); oversized {
			http.Error(w, "Resource request body too large.", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "The session server cannot be reached.", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	for _, name := range []string{"Content-Type", "Content-Length", sessionapi.ProtocolHeader, "X-Content-Type-Options"} {
		if value := response.Header.Get(name); value != "" {
			w.Header().Set(name, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	if _, err := io.Copy(w, response.Body); err != nil {
		panic(http.ErrAbortHandler)
	}
}
