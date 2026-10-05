//go:build !linux && !darwin

package router

import "time"

// Without a file change clock, sweeps are unavailable and records say so.
func execFileTimes(string) (change, birth time.Time, hasBirth, ok bool) {
	return time.Time{}, time.Time{}, false, false
}

func execRemoteFilesystem(string) bool { return true }
