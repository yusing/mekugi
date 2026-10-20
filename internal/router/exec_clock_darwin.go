//go:build darwin

package router

import (
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func execFileTimes(path string) (change, birth time.Time, hasBirth, ok bool) {
	var stat syscall.Stat_t
	if syscall.Lstat(path, &stat) != nil {
		return time.Time{}, time.Time{}, false, false
	}
	change = time.Unix(stat.Ctimespec.Sec, stat.Ctimespec.Nsec)
	birth = time.Unix(stat.Birthtimespec.Sec, stat.Birthtimespec.Nsec)
	return change, birth, true, true
}

func execRemoteFilesystem(path string) bool {
	var stat unix.Statfs_t
	if unix.Statfs(path, &stat) != nil {
		return true
	}
	name := unix.ByteSliceToString(stat.Fstypename[:])
	return name == "nfs" || name == "smbfs" || name == "afpfs" || name == "webdav" || strings.Contains(name, "fuse")
}
