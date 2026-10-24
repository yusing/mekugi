//go:build pprof

package launch

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
)

func TestWrapNativeProfilingLifetime(t *testing.T) {
	for _, outcome := range []string{"complete", "error", "cancel"} {
		t.Run(outcome, func(t *testing.T) {
			cache, err := os.MkdirTemp("", "mekugi-prof-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(cache)
			t.Setenv("XDG_CACHE_HOME", cache)
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			socket, err := sessionapi.ControlSocketPath()
			if err != nil {
				t.Fatal(err)
			}
			listener, err := sessionapi.ListenControl(socket)
			if err != nil {
				t.Fatal(err)
			}
			launcher := &nativeReattachLauncher{run: func(context.Context, sessionapi.Start, *sessionapi.Server) error {
				if outcome == "error" {
					return errors.New("profiling fixture failure")
				}
				return nil
			}}
			hub := sessionapi.NewHub(t.Context(), launcher)
			defer hub.Close()
			started, release := make(chan struct{}), make(chan struct{})
			handler := sessionapi.Versioned(hub.Handler())
			server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/version" {
					close(started)
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
				}
				handler.ServeHTTP(w, r)
			})}
			serverDone := make(chan struct{})
			go func() { defer close(serverDone); _ = server.Serve(listener) }()
			defer func() { _ = server.Close(); <-serverDone }()
			master, slave, err := pty.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer master.Close()
			readDone := make(chan struct{})
			go func() { defer close(readDone); _, _ = io.Copy(io.Discard, master) }()
			defer func() { _ = slave.Close(); <-readDone }()
			diagnostic, err := os.CreateTemp(t.TempDir(), "stderr")
			if err != nil {
				t.Fatal(err)
			}
			defer diagnostic.Close()
			stdin, stdout, stderr := os.Stdin, os.Stdout, os.Stderr
			os.Stdin, os.Stdout, os.Stderr = slave, slave, diagnostic
			defer func() { os.Stdin, os.Stdout, os.Stderr = stdin, stdout, stderr }()
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			done := make(chan struct{})
			var code int
			var runErr error
			go func() {
				defer close(done)
				code, runErr = wrapCodex(ctx, []string{"--ansi-faint=off"}, []string{"--yolo", "--model", "fixture"})
			}()
			defer func() { cancel(); <-done }()
			select {
			case <-started:
			case <-done:
				t.Fatalf("client exited before connection: %d, %v", code, runErr)
			case <-ctx.Done():
				t.Fatal("client did not connect")
			}
			// The readiness response is withheld, so no UI handoff has occurred.
			data, err := os.ReadFile(diagnostic.Name())
			if err != nil {
				t.Fatal(err)
			}
			notice := strings.TrimSpace(string(data))
			if !strings.HasPrefix(notice, "mekugi-pprof: http://127.0.0.1:") || strings.Count(notice, "mekugi-pprof:") != 1 {
				t.Fatalf("pre-handoff profiling notice: %q", notice)
			}
			url := strings.TrimPrefix(notice, "mekugi-pprof: ")
			transport := &http.Transport{Proxy: nil}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
			response, err := client.Get(url)
			if err != nil {
				t.Fatal(err)
			}
			_ = response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("profiling status: %d", response.StatusCode)
			}
			if outcome == "cancel" {
				cancel()
			} else {
				close(release)
			}
			<-done
			if outcome == "complete" {
				if code != 0 {
					t.Fatalf("client completion: %d, %v", code, runErr)
				}
			} else if code != 1 || runErr == nil {
				t.Fatalf("client failure: %d, %v", code, runErr)
			}
			if response, err := client.Get(url); err == nil {
				_ = response.Body.Close()
				t.Fatal("profiling listener survived native client exit")
			}
		})
	}
}
