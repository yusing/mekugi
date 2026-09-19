//go:build unix

package router

import (
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func shellReadOwnedFile(file *os.File) (*os.File, error) {
	raw, err := file.SyscallConn()
	if err != nil {
		return nil, err
	}
	fd := -1
	var duplicateErr error
	if err := raw.Control(func(original uintptr) {
		fd, duplicateErr = unix.FcntlInt(original, syscall.F_DUPFD_CLOEXEC, 0)
	}); err != nil {
		return nil, err
	}
	if duplicateErr != nil {
		return nil, duplicateErr
	}
	return os.NewFile(uintptr(fd), file.Name()), nil
}

func shellReadReady(file *os.File) (bool, error) {
	var ready bool
	var pollErr error
	raw, err := file.SyscallConn()
	if err != nil {
		return false, err
	}
	err = raw.Control(func(fd uintptr) {
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		n, err := unix.Poll(fds, 0)
		pollErr = err
		ready = n > 0 && fds[0].Revents&(unix.POLLIN|unix.POLLHUP) != 0
	})
	if err != nil {
		return false, err
	}
	return ready, pollErr
}
