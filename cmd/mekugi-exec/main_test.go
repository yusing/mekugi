package main

import (
	"errors"
	"io"
	"net"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestDial(t *testing.T) {
	for range 16 {
		t.Run("parallel", func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "socket")
			listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			conn, err := dial(path)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			flags, err := unix.FcntlInt(conn.Fd(), unix.F_GETFD, 0)
			if err != nil {
				t.Fatal(err)
			}
			if flags&unix.FD_CLOEXEC == 0 {
				t.Fatal("router socket is not close-on-exec")
			}
			peer, err := listener.AcceptUnix()
			if err != nil {
				t.Fatal(err)
			}
			defer peer.Close()
			if _, err := conn.Write([]byte("hello")); err != nil {
				t.Fatal(err)
			}
			data := make([]byte, 5)
			if _, err := io.ReadFull(peer, data); err != nil {
				t.Fatal(err)
			}
			if string(data) != "hello" {
				t.Fatalf("router received %q", data)
			}
		})
	}
}

func TestDialMissingSocket(t *testing.T) {
	conn, err := dial(filepath.Join(t.TempDir(), "missing"))
	if conn != nil || !errors.Is(err, unix.ENOENT) {
		t.Fatalf("dial missing socket = %v, %v; want nil, ENOENT", conn, err)
	}
}
