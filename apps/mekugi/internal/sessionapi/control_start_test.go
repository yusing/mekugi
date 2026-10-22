package sessionapi

import (
	"context"
	"errors"
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

func serveControlHandler(t *testing.T, path string, handler http.Handler) error {
	t.Helper()
	listener, err := ListenControl(path)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: handler}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		if err := <-done; !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("serve: %v", err)
		}
	})
	return nil
}

func TestEnsureControlConcurrentReadiness(t *testing.T) {
	t.Parallel()
	path := controlTestPath(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var starts atomic.Int32
	started, ready := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(ready) })
	t.Cleanup(release)
	start := func() error {
		if starts.Add(1) != 1 {
			return errors.New("duplicate launch")
		}
		err := serveControlHandler(t, path, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-ready:
				Versioned(http.NotFoundHandler()).ServeHTTP(w, r)
			case <-r.Context().Done():
			}
		}))
		close(started)
		return err
	}
	results := make(chan error, 8)
	for range 8 {
		go func() { results <- EnsureControl(ctx, path, start) }()
	}
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("server launch did not begin")
	}
	select {
	case err := <-results:
		t.Fatalf("ensure returned before readiness: %v", err)
	default:
	}
	release()
	for range 8 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if err := EnsureControl(ctx, path, start); err != nil {
		t.Fatalf("already-ready server: %v", err)
	}
	if starts.Load() != 1 {
		t.Fatalf("launches = %d, want 1", starts.Load())
	}
	if info, err := os.Stat(path + ".startup.lock"); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("startup lock admission: %v, %v", info, err)
	}
}

func TestEnsureControlIncompatible(t *testing.T) {
	t.Parallel()
	for _, status := range []int{http.StatusConflict, http.StatusOK} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			path := controlTestPath(t)
			err := serveControlHandler(t, path, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set(ProtocolHeader, ProtocolVersion)
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"ready":true,"version":"1"}`))
			}))
			if err != nil {
				t.Fatal(err)
			}
			var starts int
			err = EnsureControl(t.Context(), path, func() error { starts++; return nil })
			if err == nil || !strings.Contains(err.Error(), "incompatible") || starts != 0 {
				t.Fatalf("incompatible server: launches = %d, error = %v", starts, err)
			}
			if _, err := os.Lstat(path); err != nil {
				t.Fatalf("incompatible server socket removed: %v", err)
			}
		})
	}
}

func TestEnsureControlFailedLaunch(t *testing.T) {
	t.Parallel()
	path := controlTestPath(t)
	failed := errors.New("launch outcome unavailable")
	var starts int
	err := EnsureControl(t.Context(), path, func() error { starts++; return failed })
	if !errors.Is(err, failed) || starts != 1 {
		t.Fatalf("launches = %d, error = %v", starts, err)
	}
	// A later, explicit invocation can acquire the released startup lock.
	if err := EnsureControl(t.Context(), path, func() error {
		return serveControlHandler(t, path, Versioned(http.NotFoundHandler()))
	}); err != nil {
		t.Fatalf("startup lock remained held after failure: %v", err)
	}
}

func TestEnsureControlCancellation(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"startup lock", "readiness", "readiness response"} {
		t.Run(phase, func(t *testing.T) {
			path := controlTestPath(t)
			if phase == "startup lock" {
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				lock, err := os.OpenFile(path+".startup.lock", os.O_RDWR|os.O_CREATE, 0o600)
				if err != nil {
					t.Fatal(err)
				}
				defer lock.Close()
				if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "readiness response" {
				err := serveControlHandler(t, path, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set(ProtocolHeader, ProtocolVersion)
					w.WriteHeader(http.StatusOK)
					w.(http.Flusher).Flush()
					<-r.Context().Done()
				}))
				if err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(t.Context(), 75*time.Millisecond)
			defer cancel()
			var starts int
			err := EnsureControl(ctx, path, func() error { starts++; return nil })
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("cancelled %s: %v", phase, err)
			}
			wantStarts := 0
			if phase == "readiness" {
				wantStarts = 1
			}
			if starts != wantStarts {
				t.Fatalf("launches while cancelling %s = %d, want %d", phase, starts, wantStarts)
			}
		})
	}
}

func TestEnsureControlPrivateStartupLock(t *testing.T) {
	t.Parallel()
	path := controlTestPath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(filepath.Dir(path), "preserved")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path+".startup.lock"); err != nil {
		t.Fatal(err)
	}
	var starts int
	if err := EnsureControl(t.Context(), path, func() error { starts++; return nil }); err == nil || starts != 0 {
		t.Fatalf("symlink startup lock: launches = %d, error = %v", starts, err)
	}
	if content, err := os.ReadFile(target); err != nil || string(content) != "keep" {
		t.Fatalf("startup lock target changed: %q, %v", content, err)
	}
}
