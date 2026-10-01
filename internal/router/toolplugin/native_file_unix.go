//go:build unix

package toolplugin

import (
	"os"
	"syscall"
)

// O_NONBLOCK prevents a path swapped to a FIFO between stat and open from
// holding a Codex-owned frontend indefinitely. Callers verify the open handle.
func openNativeSource(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}
