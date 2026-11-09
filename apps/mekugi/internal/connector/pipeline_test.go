package connector

import (
	"context"
	"encoding/json/v2"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
)

// refusalWriteConn models the upstream refusing further writes after reading
// initialize, independently of when its refusal reaches the connector reader.
type refusalWriteConn struct {
	net.Conn
	refusing <-chan struct{}
	release  <-chan struct{}
	failed   chan struct{}
	once     sync.Once
}

func (c *refusalWriteConn) Read(data []byte) (int, error) {
	n, err := c.Conn.Read(data)
	select {
	case <-c.refusing:
		<-c.release
	default:
	}
	return n, err
}

func (c *refusalWriteConn) Write(data []byte) (int, error) {
	select {
	case <-c.refusing:
		c.once.Do(func() { close(c.failed) })
		return 0, io.ErrClosedPipe
	default:
		return c.Conn.Write(data)
	}
}

func TestPipelinedRefusalSurvivesUpstreamWriteFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	refusing, release, failed := make(chan struct{}), make(chan struct{}), make(chan struct{})
	releaseRefusal := sync.OnceFunc(func() { close(release) })
	const refusal = `{"jsonrpc":"2.0","id":"init","error":{"code":-32000,"message":"incompatible version","data":{"status":409,"outcome":"not_admitted"}}}`
	socket, _ := upstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{sessionapi.RPCSubprotocol}})
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.CloseNow()
		if _, _, err := conn.Read(ctx); err != nil {
			t.Error(err)
			return
		}
		close(refusing)
		// Send the refusal and close while its transport read remains gated.
		// Only the downstream read below establishes consumer delivery.
		_ = conn.Write(ctx, websocket.MessageText, []byte(refusal))
	}))
	bridge, tokenPath := connect(t, socket)
	dialUpstream := bridge.transport.DialContext
	bridge.transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := dialUpstream(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return &refusalWriteConn{Conn: conn, refusing: refusing, release: release, failed: failed}, nil
	}
	server := httptest.NewServer(bridge)
	defer func() {
		releaseRefusal()
		server.Close()
	}()
	conn := dial(t, server.URL, nil)
	message := `{"jsonrpc":"2.0","id":"init","method":"initialize","params":{"version":"incompatible","token":"` + readToken(t, tokenPath) + `"}}`
	if err := conn.Write(ctx, websocket.MessageText, []byte(message)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-refusing:
	case <-ctx.Done():
		t.Fatal("upstream did not receive initialization")
	}
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"jsonrpc":"2.0","id":"observe","method":"session/subscribe","params":{"subscription":"startup"}}`)); err != nil {
		t.Fatal(err)
	}
	// The old concurrent relay attempts this queued write and cancels before
	// the refusal is released. The ordered handshake leaves it queued instead.
	select {
	case <-failed:
		t.Log("queued upstream write failed before refusal delivery")
	case <-time.After(100 * time.Millisecond):
	}
	releaseRefusal()
	_, data, err := conn.Read(ctx)
	if err != nil || string(data) != refusal {
		t.Fatalf("initialization refusal lost: %s, %v", data, err)
	}
	if _, _, err := conn.Read(ctx); err == nil || ctx.Err() != nil {
		t.Fatalf("refused connection did not retire: %v", err)
	}
}

func TestPipelinedObservationRetainsInitializationAdmission(t *testing.T) {
	for _, admission := range []string{"accepted", "token", "version"} {
		t.Run(admission, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			hub := sessionapi.NewHub(ctx, nil)
			defer hub.Close()
			socket, _ := upstream(t, sessionapi.Versioned(hub.Handler()))
			bridge, tokenPath := connect(t, socket)
			server := httptest.NewServer(bridge)
			defer server.Close()
			conn := dial(t, server.URL, nil)
			version, token := sessionapi.ProtocolVersion, readToken(t, tokenPath)
			if admission == "token" {
				token = "refused"
			}
			if admission == "version" {
				version = "incompatible"
			}
			params, err := json.Marshal(sessionapi.RPCInitialize{Version: version, Token: token})
			if err != nil {
				t.Fatal(err)
			}
			// Both messages leave before reading the initialization response.
			for _, body := range []string{`{"jsonrpc":"2.0","id":"init","method":"initialize","params":` + string(params) + `}`, `{"jsonrpc":"2.0","id":"observe","method":"session/subscribe","params":{"subscription":"startup"}}`} {
				if err := conn.Write(ctx, websocket.MessageText, []byte(body)); err != nil {
					t.Fatal(err)
				}
			}
			read := func() sessionapi.RPCMessage {
				t.Helper()
				_, data, err := conn.Read(ctx)
				var message sessionapi.RPCMessage
				if err != nil || json.Unmarshal(data, &message) != nil {
					t.Fatalf("control reply: %v", err)
				}
				return message
			}
			first := read()
			if string(first.ID) != `"init"` {
				t.Fatal("observation preceded initialization")
			}
			if admission != "accepted" {
				status := 401
				if admission == "version" {
					status = 409
				}
				if first.Error == nil || first.Error.Data.Status != status {
					t.Fatal("incorrect initialization refusal")
				}
				if _, _, err := conn.Read(ctx); err == nil {
					t.Fatal("refused initialization admitted a queued observation")
				}
				return
			}
			var initialized sessionapi.RPCInitialize
			if first.Error != nil || json.Unmarshal(first.Result, &initialized) != nil || initialized.ServerTime == 0 {
				t.Fatal("initialization omitted confirmed clock")
			}
			if next := read(); string(next.ID) != `"observe"` || next.Error != nil {
				t.Fatal("queued observation was refused")
			}
			if next := read(); next.Method != "subscription/event" {
				t.Fatal("queued observation lost its first publication")
			}
		})
	}
}
