package sessionapi

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// EnsureControl waits for a compatible server, invoking start at most once if
// none is listening. The caller owns process startup and its outcome; this
// helper never removes sockets or stops servers. Concurrent callers serialize
// startup separately from the running server's lifetime lock.
//
// start must return promptly after attempting startup. The context bounds lock
// acquisition and readiness, including a server that accepts but never responds.
func EnsureControl(ctx context.Context, path string, start func() error) error {
	if !filepath.IsAbs(path) {
		return errors.New("server control socket path must be absolute")
	}
	transport := ControlTransport(path)
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	if ready, err := probeControl(ctx, path, client); ready || err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := private(dir); err != nil {
		return err
	}
	lock, err := os.OpenFile(path+".startup.lock", os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		_ = lock.Close()
	}()
	info, err := lock.Stat()
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || !ok || int(stat.Uid) != os.Geteuid() || info.Mode().Perm()&0o077 != 0 {
		return errors.New("server startup lock must be a private regular file owned by this user")
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return err
		}
		if err := waitControl(ctx); err != nil {
			return err
		}
	}
	if ready, err := probeControl(ctx, path, client); ready || err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := start(); err != nil {
		return fmt.Errorf("start Mekugi server: %w", err)
	}
	for {
		if ready, err := probeControl(ctx, path, client); ready || err != nil {
			return err
		}
		if err := waitControl(ctx); err != nil {
			return err
		}
	}
}

// Only an absent socket or a refused connection permits startup. Responses,
// protocol failures and ambiguous connection failures never authorize a launch.
func probeControl(ctx context.Context, path string, client *http.Client) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return false, errors.New("server control path exists and is not a socket")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://mekugi/api/version", nil)
	if err != nil {
		return false, err
	}
	request.Header.Set(ProtocolHeader, ProtocolVersion)
	response, err := client.Do(request)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) {
			return false, nil
		}
		return false, fmt.Errorf("check Mekugi server readiness: %w", err)
	}
	defer response.Body.Close()
	versions := response.Header.Values(ProtocolHeader)
	if response.StatusCode == http.StatusConflict || len(versions) != 1 || versions[0] != ProtocolVersion {
		return false, fmt.Errorf("Mekugi server uses an incompatible control protocol; version %s is required", ProtocolVersion)
	}
	if response.StatusCode != http.StatusOK {
		return false, fmt.Errorf("Mekugi server readiness returned HTTP %d", response.StatusCode)
	}
	var status struct {
		Ready   bool   `json:"ready"`
		Version string `json:"version"`
	}
	if err := json.UnmarshalRead(io.LimitReader(response.Body, 1024), &status); err != nil {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		return false, errors.New("Mekugi server returned invalid readiness data")
	}
	if status.Version != ProtocolVersion {
		return false, fmt.Errorf("Mekugi server uses an incompatible control protocol; version %s is required", ProtocolVersion)
	}
	if !status.Ready {
		return false, errors.New("Mekugi server is not ready")
	}
	return true, nil
}

func waitControl(ctx context.Context) error {
	timer := time.NewTimer(25 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
