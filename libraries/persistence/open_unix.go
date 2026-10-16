//go:build unix

package persistence

import (
	"os"
	"syscall"
)

func openRetainedFile(path string) (*os.File, error) {
	// A replaced FIFO must not wait for a writer. Unlike live source paths,
	// retained records reject final-component symlinks.
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
}
