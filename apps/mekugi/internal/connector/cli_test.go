package connector_test

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/yusing/mekugi/apps/mekugi/internal/launch"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
)

type diagnosticLines chan string

func (lines diagnosticLines) Write(data []byte) (int, error) {
	lines <- string(data)
	return len(data), nil
}

func TestConnectorCLI(t *testing.T) {
	// Signal delivery is process-wide; keep this test outside parallel execution.
	for _, tc := range []struct {
		args []string
		code int
	}{{[]string{"--help"}, 0}, {[]string{"--unknown"}, 2}, {[]string{"extra"}, 2}} {
		if code := launch.RunConnector(t.Context(), tc.args, &bytes.Buffer{}); code != tc.code {
			t.Fatalf("arguments %v: exit %d", tc.args, code)
		}
	}
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(directory, "s")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	diagnostics, done := make(diagnosticLines, 8), make(chan int, 1)
	go func() {
		done <- launch.RunConnector(ctx, []string{"--listen", "127.0.0.1:0", "--socket", socket, "--origin", "https://one.example", "--origin", "https://two.example"}, diagnostics)
	}()
	next := func() string {
		t.Helper()
		select {
		case line := <-diagnostics:
			return line
		case code := <-done:
			t.Fatalf("connector exited early: %d", code)
		case <-time.After(5 * time.Second):
			t.Fatal("connector diagnostic timed out")
		}
		return ""
	}
	ready := next()
	address, _, _ := strings.Cut(strings.TrimPrefix(ready, "Connector listening on "), ". Token file:")
	path := filepath.Join(directory, "connector", "token")
	read := func() string {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(data))
	}
	old := read()
	status := func(token, origin string) int {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		conn, _, err := websocket.Dial(ctx, "ws://"+address+sessionapi.RPCPath, &websocket.DialOptions{
			HTTPHeader: http.Header{"Origin": []string{origin}}, Subprotocols: []string{sessionapi.RPCSubprotocol},
		})
		if err != nil {
			t.Fatal(err)
		}
		defer conn.CloseNow()
		params, _ := json.Marshal(&sessionapi.RPCInitialize{Version: sessionapi.ProtocolVersion, Token: token})
		message, _ := json.Marshal(&sessionapi.RPCMessage{JSONRPC: "2.0", ID: []byte("1"), Method: "initialize", Params: params})
		if err := conn.Write(ctx, websocket.MessageText, message); err != nil {
			t.Fatal(err)
		}
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var response sessionapi.RPCMessage
		if err := json.Unmarshal(data, &response); err != nil || response.Error == nil || response.Error.Data == nil {
			t.Fatalf("invalid initialization refusal: %s, %v", data, err)
		}
		return response.Error.Data.Status
	}
	if got := status(old, "https://one.example"); got != 502 {
		t.Fatalf("unavailable server status = %d", got)
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	rotated := next()
	fresh := read()
	if old == fresh || !strings.Contains(rotated, "token rotated") || strings.Contains(ready+rotated, old) || strings.Contains(ready+rotated, fresh) {
		t.Fatal("rotation did not privately publish a replacement")
	}
	if got := status(old, "https://one.example"); got != 401 {
		t.Fatalf("old token status = %d", got)
	}
	if got := status(fresh, "https://two.example"); got != 502 {
		t.Fatalf("replacement token or second origin status = %d", got)
	}
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("canceled connector exit = %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("connector did not stop")
	}
}
