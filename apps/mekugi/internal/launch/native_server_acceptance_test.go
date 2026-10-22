package launch

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/libraries/commandargs"
)

// Exercise the consuming terminal against the real server owner. Only Codex is
// replaced: its JSON-RPC ledger proves both transports use one logical thread.
func TestNativeServerAcceptance(t *testing.T) {
	for _, key := range []string{"XDG_CONFIG_HOME", "XDG_STATE_HOME", "CODEX_HOME", "MEKUGI_RUNTIME_DIR", "TMPDIR"} {
		t.Setenv(key, t.TempDir())
	}
	for _, key := range []string{"XAI_API_KEY", "OPENCODE_API_KEY", "OPENCODE_GO_API_KEY", "OPENCODE_ZEN_API_KEY"} {
		t.Setenv(key, "")
	}
	writeFeatureConfig(t, "shell_guard = false")
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("COLORFGBG", "15;0")
	workspace := t.TempDir()
	t.Chdir(workspace)
	logPath := filepath.Join(t.TempDir(), "host.jsonl")
	t.Setenv("MEKUGI_TEST_SERVER_CLI_LOG", logPath)
	bin := t.TempDir()
	const hostDiagnostic = "synthetic host stderr warning"
	script := "#!/bin/sh\nif [ \"$1\" = debug ]; then exit 1; fi\nprintf '%s\\n' '" + hostDiagnostic + "' '" + hostDiagnostic + "' '" + hostDiagnostic + "' >&2\nMEKUGI_TEST_SERVER_CLI_HOST=1; export MEKUGI_TEST_SERVER_CLI_HOST\nexec " + commandargs.Quote(os.Args[0]) + " -test.run=^TestRunServerLifecycle$ -- \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	// Keep the Unix address below sockaddr_un limits, independently of Go's test
	// directory naming. This directory belongs solely to the fixture.
	socketDirectory, err := os.MkdirTemp("", "mekugi-native-owner-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(socketDirectory)
	socket := filepath.Join(socketDirectory, "s")
	serverContext, stopServer := context.WithCancel(t.Context())
	var diagnostic bytes.Buffer
	serverDone := make(chan int, 1)
	go func() { serverDone <- RunServer(serverContext, []string{"--socket", socket}, &diagnostic) }()
	defer func() {
		stopServer()
		select {
		case code := <-serverDone:
			if code != 0 {
				t.Errorf("server exit %d: %s", code, &diagnostic)
			}
		case <-time.After(10 * time.Second):
			t.Error("server did not join its host")
		}
	}()
	serverCLIAwait(t, func() bool { _, err := os.Stat(socket); return err == nil })
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer slave.Close()
	if err := pty.Setsize(master, &pty.Winsize{Cols: 100, Rows: 32}); err != nil {
		t.Fatal(err)
	}
	clientContext, stopClient := context.WithCancel(t.Context())
	clientDone := make(chan error, 1)
	go func() {
		clientDone <- runNativeClient(clientContext, socket, []string{"--ansi-faint=off"}, []string{"--yolo", "--model", "codex"}, slave, slave)
	}()
	joined := false
	defer func() {
		stopClient()
		if !joined {
			select {
			case <-clientDone:
			case <-time.After(10 * time.Second):
				t.Error("native client did not join")
			}
		}
	}()
	frames := make(chan []byte, 64)
	var rawTerminal bytes.Buffer
	var rawMu sync.Mutex
	rawOutput := func() string {
		rawMu.Lock()
		defer rawMu.Unlock()
		return rawTerminal.String()
	}
	readerContext, stopReader := context.WithCancel(t.Context())
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		defer close(frames)
		var pending synchronizedFrameBuffer
		buffer := make([]byte, 8192)
		for {
			n, err := master.Read(buffer)
			rawMu.Lock()
			rawTerminal.Write(buffer[:n])
			rawMu.Unlock()
			pending.Append(buffer[:n])
			for {
				frame, ok := pending.Next()
				if !ok {
					break
				}
				select {
				case frames <- frame:
				case <-readerContext.Done():
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	defer func() {
		stopReader()
		// Closing the slave releases the master's blocking PTY read on Linux.
		// Join that reader before closing its file descriptor.
		slave.Close()
		<-readerDone
		master.Close()
	}()
	screen := vt.NewEmulator(100, 32)
	repliesDone := make(chan struct{})
	go func() { defer close(repliesDone); _, _ = io.Copy(master, screen) }()
	defer func() {
		_ = screen.InputPipe().(io.Closer).Close()
		<-repliesDone
		_ = screen.Close()
	}()
	awaitScreen := func(description string, matches func(string) bool) {
		t.Helper()
		timer := time.NewTimer(10 * time.Second)
		defer timer.Stop()
		for {
			select {
			case frame, ok := <-frames:
				if !ok {
					t.Fatal("terminal reader closed")
				}
				if _, err := screen.Write(frame); err != nil {
					t.Fatal(err)
				}
				if matches(screen.String()) {
					return
				}
			case err := <-clientDone:
				joined = true
				t.Fatalf("native client exited early: %v", err)
			case <-timer.C:
				ledger, _ := os.ReadFile(logPath)
				t.Fatalf("terminal did not show %q:\n%s\nhost ledger:\n%s", description, screen.String(), ledger)
			}
		}
	}
	await := func(wants ...string) {
		t.Helper()
		awaitScreen(strings.Join(wants, ", "), func(screen string) bool {
			for _, want := range wants {
				if !strings.Contains(screen, want) {
					return false
				}
			}
			return true
		})
	}
	send := func(text string) {
		t.Helper()
		if _, err := master.WriteString(text); err != nil {
			t.Fatal(err)
		}
	}
	await("Mekugi", "main  idle")
	send("native first input\r")
	await("offline host response 1")
	transport := sessionapi.ControlTransport(socket)
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	rpc, err := sessionapi.OpenRPC(t.Context(), client, "http://mekugi"+sessionapi.RPCPath, "")
	if err != nil {
		t.Fatal(err)
	}
	defer rpc.Close()
	input := func(text string) {
		t.Helper()
		serverRPCCall(t, rpc, "session/input", "main", sessionapi.InputRequest{Text: text})
	}
	input("shared API later input")
	await("offline host response 2")
	if strings.Contains(rawOutput(), hostDiagnostic) {
		t.Fatal("host diagnostics were printed before terminal restoration")
	}
	// The command is typed through the native editor. The owner must produce the
	// status panel and perform its asynchronous account request, not a client copy.
	send("/status")
	await("Show session settings and usage limits")
	send("\r")
	await("Session status", "Session    main", "Directory")
	serverCLIAwait(t, func() bool { body, _ := os.ReadFile(logPath); return strings.Contains(string(body), "account/read ") })
	send("\x1b")
	awaitScreen("closed status panel", func(screen string) bool {
		return !strings.Contains(screen, "Session status") && strings.Contains(screen, "offline host response 2")
	})
	send("/quit")
	await("/quit")
	send("\r")
	select {
	case err := <-clientDone:
		joined = true
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("native client did not detach")
	}
	const diagnosticSummary = hostDiagnostic + " (repeated 3 times)"
	serverCLIAwait(t, func() bool { return strings.Contains(rawOutput(), diagnosticSummary) })
	raw := rawOutput()
	if restoration, diagnostic := strings.LastIndex(raw, "\x1b[?1049l"), strings.Index(raw, hostDiagnostic); restoration < 0 || diagnostic < restoration || strings.Count(raw, hostDiagnostic) != 1 {
		t.Fatalf("diagnostics were not deduplicated after alternate-screen restoration: %q", raw)
	}
	input("shared API after native detach")
	serverCLIAwait(t, func() bool {
		body, _ := os.ReadFile(logPath)
		return strings.Contains(string(body), "shared API after native detach")
	})
	ledger, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	starts, inputs := 0, 0
	for line := range strings.SplitSeq(string(ledger), "\n") {
		method, params, _ := strings.Cut(line, " ")
		if method == "thread/start" {
			starts++
		}
		if method != "turn/start" && method != "turn/steer" {
			continue
		}
		var request struct {
			Thread string `json:"threadId"`
			Input  []struct {
				Text string `json:"text"`
			} `json:"input"`
		}
		if err := json.Unmarshal([]byte(params), &request); err != nil {
			t.Fatal(err)
		}
		if request.Thread != "main" || len(request.Input) != 1 {
			t.Fatalf("unexpected host submission: %s", line)
		}
		wants := []string{"native first input", "shared API later input", "shared API after native detach"}
		if inputs >= len(wants) || request.Input[0].Text != wants[inputs] {
			t.Fatalf("unexpected host input order: %s", ledger)
		}
		inputs++
	}
	if starts != 1 || inputs != 3 {
		t.Fatalf("host ownership: starts=%d inputs=%d:\n%s", starts, inputs, ledger)
	}
}
