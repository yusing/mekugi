//go:build unix

package router

import (
	"os"
	"strconv"
	"syscall"
)

func openNativePatchFile(path string) (*os.File, error) {
	// A FIFO named by a patch must not block auxiliary observation before
	// Codex gets the stock call. Check the opened descriptor's type afterward.
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}

// execFileIdentity names the inode behind a stat result, so a replacement
// with equal size and time is not mistaken for the captured file.
func execFileIdentity(info os.FileInfo) string {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return strconv.FormatUint(uint64(stat.Dev), 10) + ":" + strconv.FormatUint(uint64(stat.Ino), 10)
	}
	return ""
}
