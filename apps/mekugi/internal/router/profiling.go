//go:build pprof

package router

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/pprof"
	"runtime"
	"time"
)

// Live sessions and offline replay share one profiling lifetime. Workers and
// hooks do not start it; concurrent processes have separate loopback URLs.
func startProfiling(ctx context.Context, stderr io.Writer) (func() error, error) {
	listener, err := net.Listen("tcp", defaultListenAddress)
	if err != nil {
		return nil, fmt.Errorf("start profiling listener: %w", err)
	}
	ctx, cancel := context.WithCancel(ctx)
	mux := http.NewServeMux()
	stopSampling := enableSessionProfiling(mux, listener.Addr().String(), stderr)
	server := &http.Server{
		Handler:           mux,
		BaseContext:       func(net.Listener) context.Context { return ctx },
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	return func() error {
		cancel()
		defer stopSampling()
		shutdownCtx, stop := context.WithTimeout(context.Background(), shutdownTimeout)
		defer stop()
		err := server.Shutdown(shutdownCtx)
		if err != nil {
			err = errors.Join(err, server.Close())
		}
		serveErr := <-done
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
		return errors.Join(err, serveErr)
	}, nil
}

func enableSessionProfiling(mux *http.ServeMux, address string, stderr io.Writer) func() {
	mux.HandleFunc("GET /debug/pprof/", pprof.Index)
	mux.HandleFunc("GET /debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("GET /debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("GET /debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("GET /debug/pprof/trace", pprof.Trace)
	// Sample one block event per millisecond of blocked time and one in ten
	// mutex contention events. CPU profiling starts only on an HTTP request.
	runtime.SetBlockProfileRate(1_000_000)
	previousMutexRate := runtime.SetMutexProfileFraction(10)
	fmt.Fprintf(stderr, "mekugi-pprof: http://%s/debug/pprof/\n", address)
	return func() {
		runtime.SetBlockProfileRate(0)
		runtime.SetMutexProfileFraction(previousMutexRate)
	}
}
