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
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/modules/diagnostics/diagnostic"
	"golang.org/x/term"
)

type nativeServerFixture struct {
	PID        int
	Terminal   bool
	ThreadEnv  bool
	RuntimeEnv bool
	StatusPipe bool
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
	if os.Getenv(nativeServerSupervisorEnvironment) != "" {
		os.Exit(Run())
	}
	if mode := os.Getenv("MEKUGI_NATIVE_SERVER_FAILURE"); mode != "" {
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, syscall.SIGUSR1)
		go func() {
			<-signals
			_, _ = os.Stderr.WriteString("private server failure evidence\n")
			if mode == "panic" {
				panic("private server panic")
			}
			os.Exit(23)
		}()
	}
	receipt := nativeServerFixture{PID: os.Getpid(), Terminal: term.IsTerminal(int(os.Stdin.Fd())) || term.IsTerminal(int(os.Stdout.Fd())), ThreadEnv: os.Getenv("CODEX_THREAD_ID") != "", RuntimeEnv: os.Getenv("MEKUGI_RUNTIME_DIR") != ""}
	var status syscall.Stat_t
	receipt.StatusPipe = syscall.Fstat(3, &status) == nil && status.Mode&syscall.S_IFMT == syscall.S_IFIFO
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

func TestNativeServerDiagnosticsLauncher(t *testing.T) {
	if socket := os.Getenv("MEKUGI_NATIVE_SERVER_LAUNCH_SOCKET"); socket != "" {
		if err := ensureNativeServer(t.Context(), socket); err != nil {
			t.Fatal(err)
		}
	}
}

func TestNativeServerDiagnosticsSurviveClientExit(t *testing.T) {
	for _, test := range []struct {
		name, mode  string
		signal      syscall.Signal
		code        int
		unavailable bool
	}{
		{name: "normal", signal: syscall.SIGTERM},
		{name: "fatal", mode: "exit", signal: syscall.SIGUSR1, code: 23},
		{name: "panic", mode: "panic", signal: syscall.SIGUSR1, code: 2},
		{name: "killed", signal: syscall.SIGKILL, code: 137},
		{name: "unavailable", signal: syscall.SIGTERM, unavailable: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory, err := os.MkdirTemp("", "mekugi-server-diagnostics-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(directory)
			socket := filepath.Join(directory, "control", "s")
			receipt := filepath.Join(directory, "started.jsonl")
			state := filepath.Join(directory, "state")
			if test.unavailable {
				if err := os.WriteFile(state, nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			client := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestNativeServerDiagnosticsLauncher$")
			client.Env = append(os.Environ(), "MEKUGI_NATIVE_SERVER_LAUNCH_SOCKET="+socket, "MEKUGI_NATIVE_SERVER_FIXTURE="+receipt, "MEKUGI_NATIVE_SERVER_FAILURE="+test.mode, "XDG_STATE_HOME="+state)
			output, err := client.CombinedOutput()
			if err != nil {
				t.Fatalf("launching client: %v\n%s", err, output)
			}
			if bytes.Contains(output, []byte("private")) || bytes.Contains(output, []byte("diagnostics unavailable")) != test.unavailable {
				t.Fatalf("ordinary client output: %s", output)
			}
			data, err := os.ReadFile(receipt)
			if err != nil {
				t.Fatal(err)
			}
			var started nativeServerFixture
			if err := json.Unmarshal(bytes.TrimSpace(data), &started); err != nil {
				t.Fatal(err)
			}
			if started.StatusPipe {
				t.Fatal("server inherited supervisor status pipe")
			}
			process, err := os.FindProcess(started.PID)
			if err != nil {
				t.Fatal(err)
			}
			defer process.Release()
			defer process.Kill()
			if err := process.Signal(test.signal); err != nil {
				t.Fatal(err)
			}
			if test.unavailable {
				for process.Signal(syscall.Signal(0)) == nil {
					select {
					case <-ctx.Done():
						t.Fatal("server did not stop without diagnostics")
					case <-time.After(10 * time.Millisecond):
					}
				}
				return
			}
			paths, err := filepath.Glob(filepath.Join(state, "mekugi", "server", "mekugi-debug-*", "process.jsonl"))
			if err != nil || len(paths) != 1 {
				t.Fatalf("process evidence paths: %v, %v", paths, err)
			}
			var events []diagnostic.ProcessEvent
			for len(events) != 2 {
				data, err = os.ReadFile(paths[0])
				if err != nil {
					t.Fatal(err)
				}
				events = nil
				for row := range bytes.Lines(data) {
					var event diagnostic.ProcessEvent
					if json.Unmarshal(row, &event) == nil {
						events = append(events, event)
					}
				}
				if len(events) != 2 {
					select {
					case <-ctx.Done():
						t.Fatalf("exit evidence missing: %s", data)
					case <-time.After(10 * time.Millisecond):
					}
				}
			}
			first, last := events[0], events[1]
			if first.Event != "start" || last.Event != "exit" || first.PID != started.PID || last.PID != started.PID || first.Build == "" || first.Executable == "" || first.GoVersion == "" || first.Socket != socket || first.Time.IsZero() || last.Time.Before(first.Time) || last.ExitCode == nil || *last.ExitCode != test.code {
				t.Fatalf("process evidence: %+v", events)
			}
			if test.signal == syscall.SIGKILL && last.Signal != "killed" {
				t.Fatalf("signal evidence: %+v", last)
			}
			if test.mode != "" {
				stderr, err := os.ReadFile(filepath.Join(filepath.Dir(paths[0]), "stderr.log"))
				if err != nil || !bytes.Contains(stderr, []byte("private server failure evidence")) || test.mode == "panic" && !bytes.Contains(stderr, []byte("panic: private server panic")) {
					t.Fatalf("private stderr: %s, %v", stderr, err)
				}
			}
		})
	}
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
