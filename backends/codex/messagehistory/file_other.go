//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !windows

package messagehistory

import (
	"errors"
	"os"
)

func openFile(string, int) (*os.File, error) {
	return nil, errors.New("Codex composer history file locking is unavailable on this platform")
}
func tryLock(*os.File, bool) (bool, error) { return false, errors.ErrUnsupported }
