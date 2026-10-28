package sessionapi

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/coder/websocket"
)

func TestWriteMessageBoundsStallsRatherThanSlowDelivery(t *testing.T) {
	t.Parallel()
	const stall = 200 * time.Millisecond
	message := bytes.Repeat([]byte("x"), 8<<20)
	for _, test := range []struct {
		name    string
		receive func(*testing.T, *websocket.Conn) error
		fails   bool
	}{
		{name: "slow", receive: func(t *testing.T, conn *websocket.Conn) error {
			_, reader, err := conn.Reader(t.Context())
			if err != nil {
				return err
			}
			var got bytes.Buffer
			chunk := make([]byte, 32<<10)
			for {
				n, err := reader.Read(chunk)
				got.Write(chunk[:n])
				if err == io.EOF {
					if !bytes.Equal(got.Bytes(), message) {
						t.Error("slow delivery changed the message")
					}
					return nil
				}
				if err != nil {
					return err
				}
				time.Sleep(5 * time.Millisecond)
			}
		}},
		{name: "stalled", fails: true, receive: func(*testing.T, *websocket.Conn) error { return nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				written := make(chan error, 1)
				server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					conn, err := websocket.Accept(w, r, nil)
					if err != nil {
						written <- err
						return
					}
					defer conn.CloseNow()
					written <- WriteMessage(r.Context(), conn, websocket.MessageText, bytes.NewReader(message), stall)
				}))
				// An unbuffered link avoids TCP receive-window and delayed-ACK stalls.
				// Virtual time controls the peer's pacing and the write stall timer.
				clientWire, serverWire := net.Pipe()
				server.Listener.Close()
				server.Listener = &rpcPipeListener{Conn: serverWire, done: make(chan struct{})}
				server.Start()
				defer server.Close()
				dial := func(ctx context.Context, network, address string) (net.Conn, error) {
					return clientWire, nil
				}
				client := &http.Client{Transport: &http.Transport{DialContext: dial}}
				conn, _, err := websocket.Dial(t.Context(), "ws"+strings.TrimPrefix(server.URL, "http"), &websocket.DialOptions{HTTPClient: client})
				if err != nil {
					t.Fatal(err)
				}
				defer conn.CloseNow()
				conn.SetReadLimit(-1)
				received := make(chan error, 1)
				go func() { received <- test.receive(t, conn) }()
				started := time.Now()
				err = <-written
				if test.fails != (err != nil) {
					t.Fatalf("write error %v after %v", err, time.Since(started))
				}
				if !test.fails && time.Since(started) < stall {
					t.Fatal("delivery finished within one stall window, so it did not exercise a slow peer")
				}
				if err := <-received; err != nil {
					t.Fatal(err)
				}
			})
		})
	}
}
