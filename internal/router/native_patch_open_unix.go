//go:build unix

package router

import (
	"os"
	"syscall"
)

func openNativePatchFile(path string) (*os.File, error) {
	// A FIFO named by a patch must not block auxiliary observation before
	// Codex gets the stock call. Check the opened descriptor's type afterward.
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}
