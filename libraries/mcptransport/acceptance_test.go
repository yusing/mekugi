package mcptransport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPIndependentEndpoints(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	start := func(name string) (string, func() error, *mcp.ClientSession) {
		t.Helper()
		server := mcp.NewServer(&mcp.Implementation{Name: name, Version: "1"}, nil)
		mcp.AddTool(server, &mcp.Tool{Name: "identity"}, func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: name}}}, nil, nil
		})
		socket, stop, err := Start(ctx, server)
		if err != nil {
			t.Fatal(err)
		}
		boundedStop := func() error {
			done := make(chan error, 1)
			go func() { done <- stop() }()
			select {
			case err := <-done:
				return err
			case <-time.After(5 * time.Second):
				return errors.New("endpoint shutdown timed out")
			}
		}
		t.Cleanup(func() {
			if err := boundedStop(); err != nil {
				t.Error(err)
			}
		})
		connection, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { connection.Close() })
		if err := connection.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatal(err)
		}
		client, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, &mcp.IOTransport{Reader: connection, Writer: connection}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return socket, boundedStop, client
	}
	assertIdentity := func(client *mcp.ClientSession, want string) {
		t.Helper()
		result, err := client.CallTool(ctx, &mcp.CallToolParams{Name: "identity", Arguments: map[string]any{}})
		if err != nil {
			t.Fatal(err)
		}
		if result.IsError || len(result.Content) != 1 {
			t.Fatalf("identity result: %+v", result)
		}
		text, ok := result.Content[0].(*mcp.TextContent)
		if !ok || text.Text != want {
			t.Fatalf("identity content: %+v, want %q", result.Content, want)
		}
	}
	firstSocket, stopFirst, first := start("first")
	secondSocket, _, second := start("second")
	if firstSocket == secondSocket || filepath.Dir(firstSocket) == filepath.Dir(secondSocket) {
		t.Fatal("invocations share endpoint resources")
	}
	assertIdentity(first, "first")
	assertIdentity(second, "second")
	if err := stopFirst(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(firstSocket)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stopped endpoint directory remains: %v", err)
	}
	assertIdentity(second, "second")
}

func TestMCPBridgeBytesAndHalfClose(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	socket := filepath.Join(t.TempDir(), "mcp.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	input, inputWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { input.Close(); inputWriter.Close() })
	outputReader, output, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { output.Close(); outputReader.Close() })
	deadline := time.Now().Add(5 * time.Second)
	for _, file := range []*os.File{inputWriter, outputReader} {
		if err := file.SetDeadline(deadline); err != nil {
			t.Fatal(err)
		}
	}
	if err := listener.SetDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	greeting := []byte{'s', 0, '\n', 0xff}
	request := []byte{0xfe, '\r', '\n', 0, 'c'}
	response := []byte{'r', 0, 0xfd, '\n'}
	serverResult := make(chan error, 1)
	go func() {
		defer close(serverResult)
		serverResult <- func() error {
			connection, err := listener.AcceptUnix()
			if err != nil {
				return err
			}
			defer connection.Close()
			if err := connection.SetDeadline(deadline); err != nil {
				return err
			}
			if _, err := connection.Write(greeting); err != nil {
				return err
			}
			got, err := io.ReadAll(connection)
			if err != nil {
				return err
			}
			if !bytes.Equal(got, request) {
				return fmt.Errorf("request bytes: %x, want %x", got, request)
			}
			// A response after read EOF requires the bridge to retain its read side.
			_, err = connection.Write(response)
			return err
		}()
	}()
	bridgeResult := make(chan error, 1)
	go func() {
		defer close(bridgeResult)
		bridgeResult <- RelayStdio(ctx, socket, input, output)
	}()
	t.Cleanup(func() {
		cancel()
		listener.Close()
		input.Close()
		output.Close()
		for _, result := range []<-chan error{serverResult, bridgeResult} {
			select {
			case err := <-result:
				if err != nil {
					t.Error(err)
				}
			case <-time.After(5 * time.Second):
				t.Error("relay worker teardown timed out")
			}
		}
	})
	// Receive unsolicited output while stdin remains open and has sent no bytes.
	gotGreeting := make([]byte, len(greeting))
	if _, err := io.ReadFull(outputReader, gotGreeting); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotGreeting, greeting) {
		t.Fatalf("greeting bytes: %x, want %x", gotGreeting, greeting)
	}
	if _, err := inputWriter.Write(request); err != nil {
		t.Fatal(err)
	}
	if err := inputWriter.Close(); err != nil {
		t.Fatal(err)
	}
	gotResponse, err := io.ReadAll(outputReader)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotResponse, response) {
		t.Fatalf("response bytes after stdin EOF: %x, want %x", gotResponse, response)
	}
	for _, result := range []<-chan error{serverResult, bridgeResult} {
		select {
		case err := <-result:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal("relay did not complete after EOF:", ctx.Err())
		}
	}
}

func TestMCPBridgeRejectsRelativeSocket(t *testing.T) {
	t.Parallel()
	input, output, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	defer output.Close()
	if err := RelayStdio(t.Context(), ".", input, output); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("relative socket path rejection: %v", err)
	}
}
