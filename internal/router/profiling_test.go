//go:build pprof

package router

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestSessionProfiling(t *testing.T) {
	var stderr bytes.Buffer
	previousMutexRate := runtime.SetMutexProfileFraction(-1)
	stop, err := startProfiling(t.Context(), &stderr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if stop != nil {
			if err := stop(); err != nil {
				t.Error(err)
			}
		}
	})
	if !strings.HasPrefix(stderr.String(), "mekugi-pprof: http://127.0.0.1:") {
		t.Fatalf("profiling notice = %q", stderr.String())
	}
	url := strings.TrimSuffix(strings.TrimSpace(strings.TrimPrefix(stderr.String(), "mekugi-pprof: ")), "/debug/pprof/")
	if got := runtime.SetMutexProfileFraction(-1); got != 10 {
		t.Fatalf("mutex sampling rate = %d", got)
	}
	client := &http.Client{Timeout: 3 * time.Second}
	for _, path := range []string{
		"/debug/pprof/", "/debug/pprof/cmdline", "/debug/pprof/symbol",
		"/debug/pprof/goroutine?debug=1", "/debug/pprof/heap",
		"/debug/pprof/allocs", "/debug/pprof/block", "/debug/pprof/mutex",
		"/debug/pprof/profile?seconds=1", "/debug/pprof/trace?seconds=0.01",
	} {
		t.Run(path, func(t *testing.T) {
			response, err := client.Get(url + path)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != http.StatusOK || len(body) == 0 {
				t.Fatalf("profile response: status %d, %d bytes", response.StatusCode, len(body))
			}
		})
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	stop = nil
	if got := runtime.SetMutexProfileFraction(-1); got != previousMutexRate {
		t.Fatalf("mutex sampling rate after stop = %d, want %d", got, previousMutexRate)
	}
	if response, err := client.Get(url + "/debug/pprof/"); err == nil {
		response.Body.Close()
		t.Fatal("profiling listener stayed open after stop")
	}
}

func TestSessionProfilingCancelsCapturesOnShutdown(t *testing.T) {
	for _, endpoint := range []string{"profile", "trace"} {
		t.Run(endpoint, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			mux := http.NewServeMux()
			stop := enableSessionProfiling(mux, "127.0.0.1:12345", io.Discard)
			defer stop()
			started := make(chan struct{})
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				close(started)
				mux.ServeHTTP(w, r)
			}))
			server.Config.BaseContext = func(net.Listener) context.Context { return ctx }
			server.Start()
			defer server.Close()
			client := &http.Client{Timeout: 3 * time.Second}
			completed := make(chan error, 1)
			go func() {
				response, err := client.Get(server.URL + "/debug/pprof/" + endpoint + "?seconds=30")
				if err == nil {
					_, err = io.Copy(io.Discard, response.Body)
					response.Body.Close()
					if response.StatusCode != http.StatusOK {
						err = fmt.Errorf("capture status = %d", response.StatusCode)
					}
				}
				completed <- err
			}()
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("capture request did not start")
			}
			cancel()
			shutdownCtx, stopShutdown := context.WithTimeout(t.Context(), 2*time.Second)
			defer stopShutdown()
			if err := server.Config.Shutdown(shutdownCtx); err != nil {
				t.Fatalf("session shutdown with active capture: %v", err)
			}
			if err := <-completed; err != nil {
				t.Fatalf("canceled capture: %v", err)
			}
		})
	}
}
