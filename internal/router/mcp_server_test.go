package router

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yusing/mekugi/internal/vcsguard"
)

func TestMCPShellHook(t *testing.T) {
	t.Parallel()
	server := newMCPServer()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	session, err := server.Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	client, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	listed, err := client.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Tools) != 1 || listed.Tools[0].Name != vcsguard.HookTool {
		t.Fatalf("independent hook catalog: %+v", listed.Tools)
	}
	visibility, err := json.Marshal(listed.Tools[0].Meta["ui"])
	if err != nil || string(visibility) != `{"visibility":["app"]}` {
		t.Fatalf("hook visibility: %s, %v", visibility, err)
	}
	for _, command := range []any{"git push '\"", 42} {
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: vcsguard.HookTool, Arguments: map[string]any{
			"helper": "/helper", "directory": "/guard", "sudo_directory": "", "tracker": "",
			"hook_event_name": "PreToolUse", "tool_name": "Bash", "tool_use_id": "item", "tool_input": map[string]any{"command": command},
		}})
		if err != nil || result.IsError {
			t.Fatalf("hook failed instead of returning a native decision: %+v, %v", result, err)
		}
		var response struct {
			Output struct {
				Decision string `json:"permissionDecision"`
				Reason   string `json:"permissionDecisionReason"`
			} `json:"hookSpecificOutput"`
		}
		if err := json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &response); err != nil {
			t.Fatal(err)
		}
		if response.Output.Decision != "deny" || response.Output.Reason == "" {
			t.Fatalf("instrumentation failure allowed: %+v", response)
		}
	}
}

func TestMCPServiceStop(t *testing.T) {
	t.Parallel()
	entered, exited := make(chan struct{}), make(chan struct{})
	server := mcp.NewServer(&mcp.Implementation{Name: "blocked", Version: "1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "blocked"}, func(ctx context.Context, _ *mcp.CallToolRequest, _ map[string]any) (*mcp.CallToolResult, any, error) {
		close(entered)
		defer close(exited)
		<-ctx.Done()
		return nil, nil, ctx.Err()
	})
	socket, stop, err := startMCP(t.Context(), server)
	if err != nil {
		t.Fatal(err)
	}
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
	<-exited
	if err := <-result; err == nil {
		t.Fatal("blocked request succeeded after shutdown")
	}
	if _, err := os.Stat(filepath.Dir(socket)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket directory remains: %v", err)
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
	go func() { done <- RunMCPBridge(ctx, socket, input, output) }()
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
