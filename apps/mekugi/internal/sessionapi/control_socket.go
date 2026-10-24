package sessionapi

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

const (
	ProtocolHeader  = "Mekugi-Protocol"
	ProtocolVersion = "3"
)

// ControlSocketPath names the server's private, user-local control endpoint.
func ControlSocketPath() (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("locate the cache directory for server control: %w", err)
	}
	return filepath.Join(cache, "mekugi", "server", "control.sock"), nil
}

// ControlTransport connects directly to a same-user server, without a network
// proxy. Callers supply ProtocolHeader and close their transport when finished.
func ControlTransport(path string) *http.Transport {
	return &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			if !filepath.IsAbs(path) {
				return nil, errors.New("server control socket path must be absolute")
			}
			if err := private(filepath.Dir(path)); err != nil {
				return nil, err
			}
			var dialer net.Dialer
			conn, err := dialer.DialContext(ctx, "unix", path)
			if err != nil {
				return nil, err
			}
			if err := ownPeer(conn); err != nil {
				_ = conn.Close()
				return nil, err
			}
			return conn, nil
		},
	}
}

// ListenControl admits same-user peers and owns the socket and its lifetime
// lock. Closing it removes only its own socket, then explicitly unlocks the
// stable lock file. Existing regular files and active sockets are never removed.
func ListenControl(path string) (net.Listener, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("server control socket path must be absolute")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := private(dir); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(path+".lock", os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0o600)
	if err != nil {
		return nil, err
	}
	owned := false
	defer func() {
		if !owned {
			_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
			_ = lock.Close()
		}
	}()
	info, err := lock.Stat()
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || !ok || int(stat.Uid) != os.Geteuid() || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("server control lock must be a private regular file owned by this user")
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errors.New("another Mekugi server already owns this control socket")
		}
		return nil, err
	}
	if err := reclaimControlSocket(path); err != nil {
		return nil, err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	listener.SetUnlinkOnClose(false)
	info, err = os.Lstat(path)
	if err != nil {
		_ = listener.Close()
		return nil, err
	}
	control := &controlListener{listener: listener, path: path, socket: info, lock: lock}
	owned = true
	if err := os.Chmod(path, 0o600); err != nil {
		_ = control.Close()
		return nil, err
	}
	return control, nil
}

// The lock prevents cooperating servers from racing. A connection probe also
// protects an active socket whose listener does not participate in that lock.
func reclaimControlSocket(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return errors.New("server control path exists and is not a socket")
	}
	conn, err := net.DialTimeout("unix", path, 250*time.Millisecond)
	if err == nil {
		_ = conn.Close()
		return errors.New("another listener already serves this control socket")
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		return fmt.Errorf("check the existing server control socket: %w", err)
	}
	current, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !os.SameFile(info, current) {
		return errors.New("server control socket changed during stale-socket admission")
	}
	return os.Remove(path)
}

type controlListener struct {
	listener *net.UnixListener
	path     string
	socket   os.FileInfo
	lock     *os.File
	once     sync.Once
	closeErr error
}

func (l *controlListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.listener.Accept()
		if err != nil {
			return nil, err
		}
		if err := ownPeer(conn); err != nil {
			_ = conn.Close()
			continue
		}
		return conn, nil
	}
}

func (l *controlListener) Addr() net.Addr { return l.listener.Addr() }

func (l *controlListener) Close() error {
	l.once.Do(func() {
		l.closeErr = l.listener.Close()
		if info, err := os.Lstat(l.path); err == nil {
			if os.SameFile(l.socket, info) {
				l.closeErr = errors.Join(l.closeErr, os.Remove(l.path))
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			l.closeErr = errors.Join(l.closeErr, err)
		}
		// Explicit unlock also releases admission when a fork retains the open
		// description until exec closes its inherited descriptor.
		l.closeErr = errors.Join(l.closeErr, syscall.Flock(int(l.lock.Fd()), syscall.LOCK_UN), l.lock.Close())
	})
	return l.closeErr
}

// Versioned rejects incompatible clients before dispatching any control route,
// including the readiness endpoint. The header advertises the supported version
// on every response without echoing request content or credentials.
func Versioned(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(ProtocolHeader, ProtocolVersion)
		versions := r.Header.Values(ProtocolHeader)
		if len(versions) != 1 || versions[0] != ProtocolVersion {
			http.Error(w, "incompatible Mekugi control protocol", http.StatusConflict)
			return
		}
		if r.URL.Path == "/api/version" {
			if r.Method != http.MethodGet {
				w.Header().Set("Allow", http.MethodGet)
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.MarshalWrite(w, &struct {
				Ready   bool   `json:"ready"`
				Version string `json:"version"`
			}{Ready: true, Version: ProtocolVersion})
			return
		}
		handler.ServeHTTP(w, r)
	})
}
