package mcptransport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPServiceStop(t *testing.T) {
	t.Parallel()
	for _, parentCancellation := range []bool{false, true} {
		t.Run(fmt.Sprintf("parent-cancellation=%t", parentCancellation), func(t *testing.T) {
			t.Parallel()
			entered, exited := make(chan struct{}), make(chan struct{})
			server := mcp.NewServer(&mcp.Implementation{Name: "blocked", Version: "1"}, nil)
			mcp.AddTool(server, &mcp.Tool{Name: "blocked"}, func(ctx context.Context, _ *mcp.CallToolRequest, _ map[string]any) (*mcp.CallToolResult, any, error) {
				close(entered)
				defer close(exited)
				<-ctx.Done()
				return nil, nil, ctx.Err()
			})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			socket, stop, err := Start(ctx, server)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := stop(); err != nil {
					t.Error(err)
				}
			})
			info, err := os.Stat(filepath.Dir(socket))
			if err != nil || info.Mode().Perm() != 0700 {
				t.Fatalf("private socket directory: %v %v", info, err)
			}
			connection, err := net.Dial("unix", socket)
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Close()
			client, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(t.Context(), &mcp.IOTransport{Reader: connection, Writer: connection}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			result := make(chan error, 1)
			go func() {
				_, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "blocked", Arguments: map[string]any{}})
				result <- err
			}()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("request did not start")
			}
			if parentCancellation {
				cancel()
				deadline := time.After(time.Second)
				tick := time.Tick(time.Millisecond)
				for {
					if _, err := os.Stat(filepath.Dir(socket)); errors.Is(err, os.ErrNotExist) {
						break
					}
					select {
					case <-deadline:
						t.Fatal("parent cancellation left the endpoint directory alive")
					case <-tick:
					}
				}
			} else {
				stopped := make(chan error, 1)
				go func() { stopped <- stop() }()
				select {
				case err := <-stopped:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(5 * time.Second):
					connection.Close()
					t.Fatal("service shutdown did not cancel its request")
				}
			}
			<-exited
			if err := <-result; err == nil {
				t.Fatal("blocked request succeeded after shutdown")
			}
			if _, err := os.Stat(filepath.Dir(socket)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("socket directory remains: %v", err)
			}
		})
	}
}

func TestMCPBridgeCancellation(t *testing.T) {
	t.Parallel()
	socket := filepath.Join(t.TempDir(), "mcp.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	input, inputWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	defer inputWriter.Close()
	outputReader, output, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	defer outputReader.Close()
	congested := make(chan error, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			congested <- err
			return
		}
		defer connection.Close()
		connection.SetWriteDeadline(time.Now().Add(time.Second))
		_, err = connection.Write(make([]byte, 8<<20))
		congested <- err
		// Keep the connection open after congestion until the bridge cancels.
		_, _ = io.Copy(io.Discard, connection)
	}()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- RelayStdio(ctx, socket, input, output) }()
	if err := <-congested; err == nil || !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("output did not become backpressured: %v", err)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("bridge cancellation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("bridge did not cancel with undrained stdout")
	}
}
