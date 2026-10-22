package sessionapi

import (
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
)

// private reports why dir must not hold the socket: whoever can create the
// socket receives every session's content and can send it input.
func private(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	switch {
	case !info.IsDir():
		return fmt.Errorf("%s is not a directory", dir)
	case !ok || int(stat.Uid) != os.Geteuid():
		return fmt.Errorf("%s belongs to another user", dir)
	case info.Mode().Perm()&0o077 != 0:
		return fmt.Errorf("%s is open to other users; make it private with chmod 700", dir)
	}
	return nil
}

// ownPeer reports why conn's other end is not this user, as the kernel
// recorded it.
func ownPeer(conn net.Conn) error {
	uid, err := peer(conn)
	if err != nil {
		return err
	}
	if uid != os.Geteuid() {
		return errors.New("the other end of the sessions socket belongs to another user")
	}
	return nil
}

// control runs f on conn's file descriptor.
func control(conn net.Conn, f func(fd int) error) error {
	unix, ok := conn.(*net.UnixConn)
	if !ok {
		return errors.New("not a Unix socket connection")
	}
	raw, err := unix.SyscallConn()
	if err != nil {
		return err
	}
	var ferr error
	if err := raw.Control(func(fd uintptr) { ferr = f(int(fd)) }); err != nil {
		return err
	}
	return ferr
}
