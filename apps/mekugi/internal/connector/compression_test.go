package connector

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/coder/websocket"
)

type measuredCompressionConn struct {
	net.Conn
	received *atomic.Int64
}

func (c measuredCompressionConn) Read(data []byte) (int, error) {
	n, err := c.Conn.Read(data)
	c.received.Add(int64(n))
	return n, err
}

// Measures the remote codec's local round-trip, allocation and wire-size tradeoff.
// Connector admission and private-leg isolation have their
// own actual-relay tests. This deliberately compressible fixture is not a link SLA.
func BenchmarkRemoteCompression(b *testing.B) {
	for _, size := range []int{32 << 10, 2_725_449} {
		for _, mode := range []websocket.CompressionMode{websocket.CompressionDisabled, websocket.CompressionNoContextTakeover} {
			b.Run(fmt.Sprintf("bytes=%d/compression=%d", size, mode), func(b *testing.B) {
				line := []byte(`{"seq":1234,"text":"retained authored content and a bounded code excerpt","running":false}` + "\n")
				payload := bytes.Repeat(line, (size+len(line)-1)/len(line))[:size]
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionNoContextTakeover})
					if err != nil {
						return
					}
					defer conn.CloseNow()
					conn.SetReadLimit(int64(size + 1))
					for {
						kind, data, err := conn.Read(b.Context())
						if err != nil {
							return
						}
						if conn.Write(b.Context(), kind, data) != nil {
							return
						}
					}
				}))
				defer server.Close()
				var received atomic.Int64
				transport := &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
					conn, err := (&net.Dialer{}).DialContext(ctx, network, address)
					if err != nil {
						return nil, err
					}
					return measuredCompressionConn{conn, &received}, nil
				}}
				defer transport.CloseIdleConnections()
				conn, _, err := websocket.Dial(b.Context(), server.URL, &websocket.DialOptions{CompressionMode: mode, HTTPClient: &http.Client{Transport: transport}})
				if err != nil {
					b.Fatal(err)
				}
				defer conn.CloseNow()
				conn.SetReadLimit(int64(size + 1))
				before := received.Load()
				b.ReportAllocs()
				b.SetBytes(int64(size))
				for b.Loop() {
					if err := conn.Write(b.Context(), websocket.MessageText, payload); err != nil {
						b.Fatal(err)
					}
					_, data, err := conn.Read(b.Context())
					if err != nil || !bytes.Equal(data, payload) {
						b.Fatalf("round trip: %v", err)
					}
				}
				b.ReportMetric(float64(received.Load()-before)/float64(b.N), "wire-B/op")
			})
		}
	}
}
