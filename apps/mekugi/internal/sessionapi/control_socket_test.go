package sessionapi

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// Keep Unix socket paths short regardless of the test name or checkout path.
func controlTestPath(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("", "mekugi-control-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return filepath.Join(root, "server", "control.sock")
}

func serveControl(t *testing.T, handler http.Handler) (*http.Client, string) {
	t.Helper()
	path := controlTestPath(t)
	listener, err := ListenControl(path)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: Versioned(handler)}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	transport := ControlTransport(path)
	t.Cleanup(func() {
		transport.CloseIdleConnections()
		_ = server.Close()
		if err := <-done; !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("serve: %v", err)
		}
	})
	return &http.Client{Transport: transport, Timeout: 5 * time.Second}, path
}

func TestControlVersionedHTTP(t *testing.T) {
	t.Parallel()
	var effects atomic.Int32
	client, _ := serveControl(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		effects.Add(1)
		w.WriteHeader(http.StatusAccepted)
	}))
	for _, test := range []struct {
		name     string
		method   string
		path     string
		versions []string
		status   int
	}{
		{"missing mutation", "POST", "/api/sessions/id/input", nil, 409},
		{"incompatible mutation", "POST", "/api/sessions/id/input", []string{"1"}, 409},
		{"duplicate version", "POST", "/api/sessions/id/input", []string{"1", "2"}, 409},
		{"missing readiness", "GET", "/api/version", nil, 409},
		{"readiness", "GET", "/api/version", []string{ProtocolVersion}, 200},
		{"readiness method", "POST", "/api/version", []string{ProtocolVersion}, 405},
		{"accepted mutation", "POST", "/api/sessions/id/input", []string{ProtocolVersion}, 202},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, err := http.NewRequestWithContext(t.Context(), test.method, "http://mekugi"+test.path, strings.NewReader(`{"text":"hello"}`))
			if err != nil {
				t.Fatal(err)
			}
			for _, version := range test.versions {
				request.Header.Add(ProtocolHeader, version)
			}
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != test.status || response.Header.Get(ProtocolHeader) != ProtocolVersion {
				t.Fatalf("status = %d, protocol = %q", response.StatusCode, response.Header.Get(ProtocolHeader))
			}
			if test.status == http.StatusOK {
				var version struct {
					Ready   bool   `json:"ready"`
					Version string `json:"version"`
				}
				if err := json.UnmarshalRead(response.Body, &version); err != nil || !version.Ready || version.Version != ProtocolVersion {
					t.Fatalf("version = %+v, decode error = %v", version, err)
				}
			}
			wantEffects := int32(0)
			if test.status == http.StatusAccepted {
				wantEffects = 1
			}
			if got := effects.Load(); got != wantEffects {
				t.Fatalf("effect count = %d, want %d", got, wantEffects)
			}
		})
	}
}

func TestControlTransportCancellation(t *testing.T) {
	t.Parallel()
	started, stopped := make(chan struct{}), make(chan struct{})
	client, path := serveControl(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(stopped)
	}))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://mekugi/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set(ProtocolHeader, ProtocolVersion)
	done := make(chan error, 1)
	go func() {
		response, err := client.Do(request)
		if response != nil {
			_ = response.Body.Close()
		}
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("request did not reach the socket server")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled request error = %v", err)
	}
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("request cancellation did not reach the server")
	}
	transport := ControlTransport(path)
	if transport.Proxy != nil {
		t.Fatal("control transport uses a network proxy")
	}
	if conn, err := transport.DialContext(ctx, "tcp", "ignored"); !errors.Is(err, context.Canceled) {
		if conn != nil {
			_ = conn.Close()
		}
		t.Fatalf("cancelled dial error = %v", err)
	}
}

func TestListenControlLifetime(t *testing.T) {
	t.Parallel()
	path := controlTestPath(t)
	var wg sync.WaitGroup
	results := make(chan net.Listener, 8)
	for range 8 {
		wg.Go(func() {
			listener, err := ListenControl(path)
			if err != nil && !strings.Contains(err.Error(), "already owns") {
				t.Errorf("listen: %v", err)
			}
			results <- listener
		})
	}
	wg.Wait()
	close(results)
	var listeners []net.Listener
	for listener := range results {
		if listener != nil {
			listeners = append(listeners, listener)
			t.Cleanup(func() { _ = listener.Close() })
		}
	}
	if len(listeners) != 1 {
		t.Fatalf("active listeners = %d, want 1", len(listeners))
	}
	for target, mode := range map[string]os.FileMode{filepath.Dir(path): 0o700, path: 0o600, path + ".lock": 0o600} {
		info, err := os.Stat(target)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != mode {
			t.Errorf("%s permissions = %o, want %o", target, info.Mode().Perm(), mode)
		}
	}
	listener := listeners[0].(*controlListener)
	inherited, err := syscall.Dup(int(listener.lock.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(inherited)
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("closed socket still exists: %v", err)
	}
	next, err := ListenControl(path)
	if err != nil {
		t.Fatalf("closed listener retained admission: %v", err)
	}
	t.Cleanup(func() { _ = next.Close() })
	if err := listener.Close(); err != nil {
		t.Fatalf("repeated close: %v", err)
	}
	if conn, err := net.Dial("unix", path); err != nil {
		t.Fatalf("repeated close removed the successor: %v", err)
	} else {
		_ = conn.Close()
	}
}

func TestListenControlReclamation(t *testing.T) {
	t.Parallel()
	path := controlTestPath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if listener, err := ListenControl(path); err == nil {
		_ = listener.Close()
		t.Fatal("regular file was admitted as a stale socket")
	}
	if content, err := os.ReadFile(path); err != nil || string(content) != "keep" {
		t.Fatalf("regular file changed: %q, %v", content, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	active, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	active.SetUnlinkOnClose(false)
	t.Cleanup(func() { _ = active.Close() })
	if listener, err := ListenControl(path); err == nil {
		_ = listener.Close()
		t.Fatal("active unregistered socket was replaced")
	}
	if conn, err := net.Dial("unix", path); err != nil {
		t.Fatalf("active socket was removed: %v", err)
	} else {
		_ = conn.Close()
	}
	_ = active.Close()
	listener, err := ListenControl(path)
	if err != nil {
		t.Fatalf("reclaim stale socket: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if content, err := os.ReadFile(path); err != nil || string(content) != "replacement" {
		t.Fatalf("close removed a replacement: %q, %v", content, err)
	}
}

func TestControlSocketPrivatePaths(t *testing.T) {
	t.Parallel()
	path := controlTestPath(t)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if listener, err := ListenControl(path); err == nil {
		_ = listener.Close()
		t.Fatal("listener accepted a shared directory")
	}
	if conn, err := ControlTransport(path).DialContext(t.Context(), "tcp", "ignored"); err == nil {
		_ = conn.Close()
		t.Fatal("client accepted a shared directory")
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path+".lock"); err != nil {
		t.Fatal(err)
	}
	if listener, err := ListenControl(path); err == nil {
		_ = listener.Close()
		t.Fatal("listener followed a lock symlink")
	}
	if contents, err := os.ReadFile(target); err != nil || string(contents) != "keep" {
		t.Fatalf("lock symlink target changed: %q, %v", contents, err)
	}
}
