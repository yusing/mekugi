package connector

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
)

func TestConnectorCancellationReleasesBlockedRelay(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"rotate", "close"} {
		t.Run(operation, func(t *testing.T) {
			upstreamDone, handlerDone := make(chan struct{}), make(chan struct{})
			socket := rpcUpstream(t, func(conn *websocket.Conn) {
				defer close(upstreamDone)
				message := []byte(`{"jsonrpc":"2.0","method":"subscription/event","params":{"data":"` + strings.Repeat("x", 32<<10) + `"}}`)
				for conn.Write(t.Context(), websocket.MessageText, message) == nil {
				}
			})
			c, path := connect(t, socket)
			remote := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(handlerDone)
				c.ServeHTTP(w, r)
			}))
			remote.Config.ConnState = func(conn net.Conn, state http.ConnState) {
				if state == http.StateNew {
					if err := conn.(*net.TCPConn).SetWriteBuffer(1024); err != nil {
						t.Error(err)
					}
				}
			}
			remote.Start()
			defer remote.Close()
			transport := &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
				conn, err := (&net.Dialer{}).DialContext(ctx, network, address)
				if err == nil {
					err = conn.(*net.TCPConn).SetReadBuffer(1024)
				}
				return conn, err
			}}
			defer transport.CloseIdleConnections()
			conn, _, err := websocket.Dial(t.Context(), remote.URL+sessionapi.RPCPath, &websocket.DialOptions{
				HTTPClient: &http.Client{Transport: transport}, Subprotocols: []string{sessionapi.RPCSubprotocol},
			})
			if err != nil {
				t.Fatal(err)
			}
			defer conn.CloseNow()
			if reply := initialize(t, conn, readToken(t, path)); reply.Error != nil {
				t.Fatal(reply.Error)
			}
			// Stop consuming the actual TCP stream and fill the small buffers.
			time.Sleep(200 * time.Millisecond)
			if operation == "rotate" {
				err = c.Rotate()
			} else {
				err = c.Close()
			}
			if err != nil {
				t.Fatal(err)
			}
			for label, done := range map[string]<-chan struct{}{"upstream": upstreamDone, "relay handler": handlerDone} {
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Errorf("cancellation left %s blocked", label)
				}
			}
		})
	}
}

func TestConnectorInitializationAndConnectionBounds(t *testing.T) {
	t.Parallel()
	socket := rpcUpstream(t, func(conn *websocket.Conn) { echo(t, conn) })
	c, path := connect(t, socket)
	remote := httptest.NewServer(c)
	defer remote.Close()
	var connections []*websocket.Conn
	defer func() {
		for _, conn := range connections {
			conn.CloseNow()
		}
	}()
	for range unauthenticatedLimit {
		connections = append(connections, dial(t, remote.URL, nil))
	}
	refused := func() {
		t.Helper()
		conn, response, err := websocket.Dial(t.Context(), remote.URL+sessionapi.RPCPath, &websocket.DialOptions{Subprotocols: []string{sessionapi.RPCSubprotocol}})
		if conn != nil {
			conn.CloseNow()
		}
		if response != nil {
			defer response.Body.Close()
		}
		if err == nil || response == nil || response.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("connection limit: response %v, error %v", response, err)
		}
	}
	refused()
	token := readToken(t, path)
	for _, conn := range connections {
		if reply := initialize(t, conn, token); reply.Error != nil {
			t.Fatal(reply.Error)
		}
	}
	for len(connections) < connectionLimit {
		conn := dial(t, remote.URL, nil)
		connections = append(connections, conn)
		if reply := initialize(t, conn, token); reply.Error != nil {
			t.Fatal(reply.Error)
		}
	}
	refused()
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.connections != 0 || c.unauthenticated != 0 {
		t.Fatalf("Close left %d connections, %d unauthenticated", c.connections, c.unauthenticated)
	}
}

func TestConnectorInitializationDeadline(t *testing.T) {
	t.Parallel()
	c, _ := connect(t, "/absent/socket")
	remote := httptest.NewServer(c)
	defer remote.Close()
	conn := dial(t, remote.URL, nil)
	defer conn.CloseNow()
	ctx, cancel := context.WithTimeout(t.Context(), initializeTimeout+2*time.Second)
	defer cancel()
	if _, _, err := conn.Read(ctx); err == nil || ctx.Err() != nil {
		t.Fatalf("initialization was not closed by its own deadline: %v", err)
	}
}

func TestConnectorRotationCancelsPendingUpstreamHandshake(t *testing.T) {
	t.Parallel()
	started, canceled := make(chan struct{}), make(chan struct{})
	socket, _ := upstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(canceled)
	}))
	c, path := connect(t, socket)
	remote := httptest.NewServer(c)
	defer remote.Close()
	conn := dial(t, remote.URL, nil)
	defer conn.CloseNow()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	message := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"version":"` + sessionapi.ProtocolVersion + `","token":"` + readToken(t, path) + `"}}`
	if err := conn.Write(ctx, websocket.MessageText, []byte(message)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("upstream handshake did not start")
	}
	if err := c.Rotate(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-canceled:
	case <-ctx.Done():
		t.Fatal("rotation left upstream handshake pending")
	}
	if _, data, err := conn.Read(ctx); err == nil || ctx.Err() != nil {
		t.Fatalf("rotation did not close downstream: message %s, read error %v", data, err)
	}
}
