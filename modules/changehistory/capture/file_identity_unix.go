//go:build unix

package capture

// Source: apps/mekugi/internal/router/exec_file_identity_unix.go:1:18@1e6d58c6.

import (
	"os"
	"strconv"
	"syscall"
)

// execFileIdentity names the inode behind a stat result, so a replacement
// with equal size and time is not mistaken for the captured file.
func execFileIdentity(info os.FileInfo) string {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return strconv.FormatUint(uint64(stat.Dev), 10) + ":" + strconv.FormatUint(uint64(stat.Ino), 10)
	}
	return ""
}
