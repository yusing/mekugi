package persistence

import (
	"os"
	"syscall"
)

func openRetainedFile(path string) (*os.File, error) {
	// WASI's Go opener exposes no-follow but no open-time nonblocking flag.
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
}
