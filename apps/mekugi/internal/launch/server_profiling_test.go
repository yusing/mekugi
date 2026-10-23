//go:build pprof

package launch

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunServerProfilingBeforeSession(t *testing.T) {
	socketDirectory := t.TempDir()
	if err := os.Chmod(socketDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(socketDirectory, "s")
	ctx, cancel := context.WithCancel(t.Context())
	stderr, diagnostic := io.Pipe()
	firstLine := make(chan string, 1)
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		scanner := bufio.NewScanner(stderr)
		if scanner.Scan() {
			firstLine <- scanner.Text()
		}
		// Keep draining startup and shutdown diagnostics without sharing a buffer.
		_, _ = io.Copy(io.Discard, stderr)
	}()
	done := make(chan int, 1)
	go func() { done <- RunServer(ctx, []string{"--socket", socket}, diagnostic) }()
	joined := false
	t.Cleanup(func() {
		cancel()
		if !joined {
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Error("server did not join after cancellation")
			}
		}
		_ = diagnostic.Close()
		_ = stderr.Close()
		<-drained
	})

	var notice string
	select {
	case notice = <-firstLine:
	case code := <-done:
		joined = true
		t.Fatalf("server exited before announcing profiling: %d", code)
	case <-time.After(10 * time.Second):
		t.Fatal("server did not announce profiling before session admission")
	}
	if !strings.HasPrefix(notice, "mekugi-pprof: http://127.0.0.1:") {
		t.Fatalf("first startup diagnostic: %q", notice)
	}
	url := strings.TrimPrefix(notice, "mekugi-pprof: ")
	transport := &http.Transport{Proxy: nil}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	// No control client has connected and no Codex or model session exists.
	for _, endpoint := range []string{"", "heap"} {
		response, err := client.Get(url + endpoint)
		if err != nil {
			t.Fatal(err)
		}
		body, readErr := io.ReadAll(response.Body)
		closeErr := response.Body.Close()
		if readErr != nil || closeErr != nil || response.StatusCode != http.StatusOK || len(body) == 0 {
			t.Fatalf("profiling endpoint %q: status=%d bytes=%d read=%v close=%v", endpoint, response.StatusCode, len(body), readErr, closeErr)
		}
	}

	cancel()
	select {
	case code := <-done:
		joined = true
		if code != 0 {
			t.Fatalf("server exit code: %d", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("server did not join after cancellation")
	}
	response, err := client.Get(url)
	if err == nil {
		_ = response.Body.Close()
		t.Fatal("profiling listener stayed open after server exit")
	}
}
