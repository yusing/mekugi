//go:build linux

package router

import (
	"time"

	"golang.org/x/sys/unix"
)

// execFileTimes reads a path's change time and, where the filesystem records
// one, its birth time, without following a final symlink.
func execFileTimes(path string) (change, birth time.Time, hasBirth, ok bool) {
	var stat unix.Statx_t
	if unix.Statx(unix.AT_FDCWD, path, unix.AT_SYMLINK_NOFOLLOW, unix.STATX_CTIME|unix.STATX_BTIME, &stat) != nil ||
		stat.Mask&unix.STATX_CTIME == 0 {
		return time.Time{}, time.Time{}, false, false
	}
	change = time.Unix(stat.Ctime.Sec, int64(stat.Ctime.Nsec))
	if stat.Mask&unix.STATX_BTIME != 0 {
		birth, hasBirth = time.Unix(stat.Btime.Sec, int64(stat.Btime.Nsec)), true
	}
	return change, birth, hasBirth, true
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
