//go:build unix

// Package fileidentity names filesystem objects for retained observations.
package fileidentity

import (
	"os"
	"strconv"
	"syscall"
)

// FromInfo distinguishes replacement objects with equal size and time.
func FromInfo(info os.FileInfo) string {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return strconv.FormatUint(uint64(stat.Dev), 10) + ":" + strconv.FormatUint(uint64(stat.Ino), 10)
	}
	return ""
}
