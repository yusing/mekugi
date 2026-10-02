//go:build unix && !linux

package main

import (
	"syscall"

	"golang.org/x/sys/unix"
)

func newUnixSocket() (int, error) {
	// Darwin has no SOCK_CLOEXEC. Hold the fork lock until the descriptor is
	// close-on-exec so a concurrent os/exec cannot inherit it. The caller
	// connects after releasing the lock, since Connect may block.
	syscall.ForkLock.RLock()
	fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err == nil {
		_, err = unix.FcntlInt(uintptr(fd), unix.F_SETFD, unix.FD_CLOEXEC)
		if err != nil {
			unix.Close(fd)
		}
	}
	syscall.ForkLock.RUnlock()
	return fd, err
}
