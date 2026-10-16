//go:build !linux && !darwin

package capture

// Source: apps/mekugi/internal/router/exec_clock_other.go:1:12@1e6d58c6.

import "time"

// Without a file change clock, sweeps are unavailable and records say so.
func execFileTimes(string) (change time.Time, ok bool) {
	return time.Time{}, false
}

func execRemoteFilesystem(string) bool { return true }
