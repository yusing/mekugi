//go:build linux

package capture

// Source: apps/mekugi/internal/router/exec_clock_linux.go:1:46@1e6d58c6.

import (
	"time"

	"golang.org/x/sys/unix"
)

// execFileTimes reads a path's change time without following a final symlink.
func execFileTimes(path string) (change time.Time, ok bool) {
	var stat unix.Statx_t
	if unix.Statx(unix.AT_FDCWD, path, unix.AT_SYMLINK_NOFOLLOW, unix.STATX_CTIME, &stat) != nil ||
		stat.Mask&unix.STATX_CTIME == 0 {
		return time.Time{}, false
	}
	change = time.Unix(stat.Ctime.Sec, int64(stat.Ctime.Nsec))
	return change, true
}

// execRemoteFilesystem reports roots whose change times come from another
// host's clock, so a sweep there cannot be compared with the local marker.
func execRemoteFilesystem(path string) bool {
	var stat unix.Statfs_t
	if unix.Statfs(path, &stat) != nil {
		return true
	}
	switch uint32(stat.Type) {
	case 0x6969, // NFS
		0x517b,     // SMB
		0xff534d42, // CIFS
		0xfe534d42, // SMB2
		0x65735546, // FUSE
		0x01021997, // 9P
		0x00c36400, // Ceph
		0x5346414f, // AFS
		0x73757245: // Coda
		return true
	}
	return false
}
