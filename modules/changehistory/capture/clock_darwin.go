//go:build darwin

package capture

// Source: apps/mekugi/internal/router/exec_clock_darwin.go:1:30@1e6d58c6.

import (
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func execFileTimes(path string) (change time.Time, ok bool) {
	var stat syscall.Stat_t
	if syscall.Lstat(path, &stat) != nil {
		return time.Time{}, false
	}
	change = time.Unix(stat.Ctimespec.Sec, stat.Ctimespec.Nsec)
	return change, true
}

func execRemoteFilesystem(path string) bool {
	var stat unix.Statfs_t
	if unix.Statfs(path, &stat) != nil {
		return true
	}
	name := unix.ByteSliceToString(stat.Fstypename[:])
	return name == "nfs" || name == "smbfs" || name == "afpfs" || name == "webdav" || strings.Contains(name, "fuse")
}
