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
	"time"

	"github.com/coder/websocket"
)

func TestWriteMessageBoundsStallsRatherThanSlowDelivery(t *testing.T) {
	t.Parallel()
	const stall = 200 * time.Millisecond
	message := bytes.Repeat([]byte("x"), 8<<20)
	for _, test := range []struct {
		name    string
		receive func(*websocket.Conn) error
		fails   bool
	}{
		{name: "slow", receive: func(conn *websocket.Conn) error {
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
		{name: "stalled", fails: true, receive: func(*websocket.Conn) error { return nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
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
			// Small socket buffers keep the kernel from absorbing the message.
			server.Listener = smallBuffers{server.Listener}
			server.Start()
			defer server.Close()
			dial := func(ctx context.Context, network, address string) (net.Conn, error) {
				conn, err := (&net.Dialer{}).DialContext(ctx, network, address)
				if err == nil {
					err = conn.(*net.TCPConn).SetReadBuffer(16 << 10)
				}
				return conn, err
			}
			client := &http.Client{Transport: &http.Transport{DialContext: dial}}
			conn, _, err := websocket.Dial(t.Context(), "ws"+strings.TrimPrefix(server.URL, "http"), &websocket.DialOptions{HTTPClient: client})
			if err != nil {
				t.Fatal(err)
			}
			defer conn.CloseNow()
			conn.SetReadLimit(-1)
			go test.receive(conn)
			started := time.Now()
			err = <-written
			if test.fails != (err != nil) {
				t.Fatalf("write error %v after %v", err, time.Since(started))
			}
			if !test.fails && time.Since(started) < stall {
				t.Fatal("delivery finished within one stall window, so it did not exercise a slow peer")
			}
		})
	}
}

type smallBuffers struct{ net.Listener }

func (l smallBuffers) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err == nil {
		err = conn.(*net.TCPConn).SetWriteBuffer(16 << 10)
	}
	return conn, err
}
