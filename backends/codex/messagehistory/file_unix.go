//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package messagehistory

import (
	"errors"
	"os"
	"syscall"
)

func openFile(path string, flags int) (*os.File, error) {
	return os.OpenFile(path, flags|syscall.O_NONBLOCK, 0600)
}
func tryLock(f *os.File, write bool) (bool, error) {
	mode := syscall.LOCK_SH
	if write {
		mode = syscall.LOCK_EX
	}
	err := syscall.Flock(int(f.Fd()), mode|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return false, nil
	}
	return err == nil, err
}
