package launch

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"golang.org/x/term"
)

type nativeServerFixture struct {
	PID        int
	Terminal   bool
	ThreadEnv  bool
	RuntimeEnv bool
}

func TestNativeStartupInterruptProcess(t *testing.T) {
	if os.Getenv("MEKUGI_NATIVE_START_INTERRUPT_FIXTURE") == "1" {
		os.Exit(runWrap([]string{"--ansi-faint=off"}, []string{"codex", "--yolo"}))
	}
}

func TestNativeStartupInterruptCancelsReadinessWait(t *testing.T) {
	cache, err := os.MkdirTemp("", "mekugi-interrupt-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(cache)
	t.Setenv("XDG_CACHE_HOME", cache)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("MEKUGI_NATIVE_START_INTERRUPT_FIXTURE", "1")
	socket, err := sessionapi.ControlSocketPath()
	if err != nil {
		t.Fatal(err)
	}
	listener, err := sessionapi.ListenControl(socket)
	if err != nil {
		t.Fatal(err)
	}
	started, canceled := make(chan struct{}), make(chan struct{})
	var once sync.Once
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(started) })
		<-r.Context().Done()
		close(canceled)
	})}
	serverDone := make(chan struct{})
	go func() { defer close(serverDone); _ = server.Serve(listener) }()
	defer func() { _ = server.Close(); <-serverDone }()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestNativeStartupInterruptProcess$")
	terminal, err := pty.Start(command)
	if err != nil {
		t.Fatal(err)
	}
	defer terminal.Close()
	output := make(chan []byte, 1)
	go func() { data, _ := io.ReadAll(terminal); output <- data }()
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case <-started:
	case err := <-done:
		t.Fatalf("native startup exited before readiness: %v\n%s", err, <-output)
	case <-ctx.Done():
		t.Fatal("native startup did not probe readiness")
	}
	if err := command.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err == nil || ctx.Err() != nil {
		t.Fatalf("startup did not cancel promptly: %v, %v", err, ctx.Err())
	}
	if data := <-output; !bytes.Contains(data, []byte("interrupt signal received")) {
		t.Fatalf("startup cancellation diagnostic: %s", data)
	}
	select {
	case <-canceled:
	case <-ctx.Done():
		t.Fatal("readiness request survived client interruption")
	}
}

func runNativeServerFixture() {
	receipt := nativeServerFixture{PID: os.Getpid(), Terminal: term.IsTerminal(int(os.Stdin.Fd())) || term.IsTerminal(int(os.Stdout.Fd())), ThreadEnv: os.Getenv("CODEX_THREAD_ID") != "", RuntimeEnv: os.Getenv("MEKUGI_RUNTIME_DIR") != ""}
	data, err := json.Marshal(receipt)
	if err != nil {
		os.Exit(91)
	}
	file, err := os.OpenFile(os.Getenv("MEKUGI_NATIVE_SERVER_FIXTURE"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		os.Exit(92)
	}
	_, err = file.Write(append(data, '\n'))
	if err = errors.Join(err, file.Close()); err != nil {
		os.Exit(93)
	}
	os.Exit(Run())
}

func TestNativeServerProcessStartupRaceAndIndependentLifetime(t *testing.T) {
	directory, err := os.MkdirTemp("", "mekugi-autostart-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(directory)
	receipts := filepath.Join(directory, "started.jsonl")
	socket := filepath.Join(directory, "control", "s")
	t.Setenv("MEKUGI_NATIVE_SERVER_FIXTURE", receipts)
	t.Setenv("CODEX_THREAD_ID", "borrowed-parent")
	t.Setenv("MEKUGI_RUNTIME_DIR", filepath.Join(directory, "parent-only"))
	readReceipts := func() []nativeServerFixture {
		t.Helper()
		data, err := os.ReadFile(receipts)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		var result []nativeServerFixture
		for line := range bytes.Lines(data) {
			var item nativeServerFixture
			if err := json.Unmarshal(line, &item); err != nil {
				t.Fatal(err)
			}
			result = append(result, item)
		}
		return result
	}
	defer func() {
		for _, receipt := range readReceipts() {
			process, err := os.FindProcess(receipt.PID)
			if err != nil {
				continue
			}
			_ = process.Signal(syscall.SIGTERM)
			deadline := time.Now().Add(10 * time.Second)
			for process.Signal(syscall.Signal(0)) == nil && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if process.Signal(syscall.Signal(0)) == nil {
				_ = process.Kill()
				t.Error("fixture server did not stop after SIGTERM: " + strconv.Itoa(receipt.PID))
			}
			_ = process.Release()
		}
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	errors := make(chan error, 6)
	var callers sync.WaitGroup
	for range cap(errors) {
		callers.Go(func() { errors <- ensureNativeServer(ctx, socket) })
	}
	callers.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	cancel()
	started := readReceipts()
	if len(started) != 1 || started[0].Terminal || started[0].ThreadEnv || started[0].RuntimeEnv {
		t.Fatalf("detached startup ownership: %+v", started)
	}
	transport := sessionapi.ControlTransport(socket)
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	request, err := http.NewRequestWithContext(t.Context(), "GET", "http://mekugi/api/version", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set(sessionapi.ProtocolHeader, sessionapi.ProtocolVersion)
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("client cancellation stopped the server: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("server stopped serving after clients left: %s", response.Status)
	}
}
