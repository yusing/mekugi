package messagehistory

import (
	"errors"
	"golang.org/x/sys/windows"
	"os"
)

func openFile(path string, flags int) (*os.File, error) {
	return os.OpenFile(path, flags, 0600)
}
func tryLock(f *os.File, write bool) (bool, error) {
	flags := uint32(windows.LOCKFILE_FAIL_IMMEDIATELY)
	if write {
		flags |= windows.LOCKFILE_EXCLUSIVE_LOCK
	}
	err := windows.LockFileEx(windows.Handle(f.Fd()), flags, 0, ^uint32(0), ^uint32(0), &windows.Overlapped{})
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return false, nil
	}
	return err == nil, err
}
