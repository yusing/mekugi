package launch

import (
	"bytes"
	"context"
	"encoding/base64"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/connector"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/libraries/commandargs"
)

func TestConnectorServerAttachmentInputAndDisconnect(t *testing.T) {
	socketDirectory, err := os.MkdirTemp("", "mekugi-bridge-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(socketDirectory)
	for _, key := range []string{"XDG_CONFIG_HOME", "XDG_STATE_HOME", "CODEX_HOME", "MEKUGI_RUNTIME_DIR", "TMPDIR"} {
		t.Setenv(key, t.TempDir())
	}
	for _, key := range []string{"XAI_API_KEY", "OPENCODE_API_KEY", "OPENCODE_GO_API_KEY", "OPENCODE_ZEN_API_KEY"} {
		t.Setenv(key, "")
	}
	writeFeatureConfig(t, "shell_guard = false")
	logPath := filepath.Join(t.TempDir(), "host.jsonl")
	t.Setenv("MEKUGI_TEST_SERVER_CLI_LOG", logPath)
	bin := t.TempDir()
	script := "#!/bin/sh\nif [ \"$1\" = debug ]; then exit 1; fi\nMEKUGI_TEST_SERVER_CLI_HOST=1; export MEKUGI_TEST_SERVER_CLI_HOST\nexec " + commandargs.Quote(os.Args[0]) + " -test.run=^TestRunServerLifecycle$ -- \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithCancel(t.Context())
	socket := filepath.Join(socketDirectory, "s")
	done := make(chan int, 1)
	var diagnostics bytes.Buffer
	go func() { done <- RunServer(ctx, []string{"--socket", socket}, &diagnostics) }()
	defer func() {
		cancel()
		select {
		case code := <-done:
			if code != 0 {
				t.Errorf("server exit %d: %s", code, &diagnostics)
			}
		case <-time.After(10 * time.Second):
			t.Error("server did not join its owned runtime")
		}
	}()
	serverCLIAwait(t, func() bool { _, err := os.Stat(socket); return err == nil })
	tokenFile := filepath.Join(t.TempDir(), "private", "token")
	bridge, err := connector.New(connector.Options{SocketPath: socket, TokenFile: tokenFile, Origins: []string{"https://ui.example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.Close()
	remote := httptest.NewServer(bridge)
	defer remote.Close()
	credential, err := os.ReadFile(tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	rpc, err := sessionapi.OpenRPC(t.Context(), client, remote.URL+sessionapi.RPCPath, strings.TrimSpace(string(credential)))
	if err != nil {
		t.Fatal(err)
	}
	defer rpc.Close()
	workspace := t.TempDir()
	hostFile := filepath.Join(workspace, "host.txt")
	if err := os.WriteFile(hostFile, []byte("execution-machine file bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	image, err := os.ReadFile(pasteTestImage(t))
	if err != nil {
		t.Fatal(err)
	}
	data := serverRPCCall(t, rpc, "session/start", "", sessionapi.Start{Workspace: workspace, Model: "codex", Attachments: []sessionapi.Attachment{
		{Source: "upload", Name: "first.txt", Data: new(base64.StdEncoding.EncodeToString([]byte("first uploaded bytes")))},
		{Source: "path", Path: "host.txt"},
		{Source: "upload", Name: "../../display-label.png", Data: new(base64.StdEncoding.EncodeToString(image))},
	}})
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(data, &created); err != nil || created.ID != "main" {
		t.Fatalf("created logical thread: %q, %v", created.ID, err)
	}
	events := serverRPCSubscribe(t, rpc, created.ID)
	serverCLIEvent(t, events, "", "offline host response 1")
	serverRPCCall(t, rpc, "session/input", created.ID, sessionapi.InputRequest{Attachments: []sessionapi.Attachment{{Source: "upload", Name: "later.txt", Data: new(base64.StdEncoding.EncodeToString([]byte("later uploaded bytes")))}}})
	serverCLIEvent(t, events, "patch", "offline host response 2")
	events.Close()
	rpc.Close()
	remote.Close()
	if err := bridge.Close(); err != nil {
		t.Fatal(err)
	}
	transport := sessionapi.ControlTransport(socket)
	defer transport.CloseIdleConnections()
	local := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	localRPC, err := sessionapi.OpenRPC(t.Context(), local, "http://mekugi"+sessionapi.RPCPath, "")
	if err != nil {
		t.Fatal(err)
	}
	defer localRPC.Close()
	serverRPCCall(t, localRPC, "session/input", created.ID, sessionapi.InputRequest{Text: "continues after connector disconnect"})
	serverCLIAwait(t, func() bool {
		body, _ := os.ReadFile(logPath)
		return strings.Contains(string(body), "continues after connector disconnect")
	})
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(log), "first uploaded bytes") || !strings.Contains(string(log), "execution-machine file bytes") || !strings.Contains(string(log), "later uploaded bytes") {
		t.Fatal("connector/socket/server input lost an attachment snapshot")
	}
	for line := range strings.SplitSeq(string(log), "\n") {
		method, params, _ := strings.Cut(line, " ")
		if method != "turn/start" {
			continue
		}
		var submission struct{ Input []struct{ Type, Path string } }
		if err := json.Unmarshal([]byte(params), &submission, json.MatchCaseInsensitiveNames(true)); err != nil {
			t.Fatal(err)
		}
		for _, input := range submission.Input {
			if input.Type != "localImage" {
				continue
			}
			defer os.Remove(input.Path)
			copied, err := os.ReadFile(input.Path)
			if err != nil || !bytes.Equal(copied, image) {
				t.Fatalf("server image did not survive connector disconnect: %v", err)
			}
			return
		}
	}
	t.Fatal("initial image never reached the host input")
}
