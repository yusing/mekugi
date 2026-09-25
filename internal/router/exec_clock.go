package router

import (
	"os"
	"time"
)

// execWindowStart reads the file clock by creating a marker in dir, so the
// window compares file times with file times rather than with the router's
// clock.
func execWindowStart(dir string) time.Time {
	marker, err := os.CreateTemp(dir, ".exec-window-*")
	if err != nil {
		return time.Time{}
	}
	name := marker.Name()
	defer marker.Close()
	defer os.Remove(name)
	initial, _, _, ok := execFileTimes(name)
	if !ok {
		return time.Time{}
	}
	// Inode timestamps may share a coarse kernel tick. Advance the marker
	// beyond its first tick so unchanged files immediately preceding capture
	// cannot be mistaken for command effects by the inclusive comparison.
	deadline := time.Now().Add(20 * time.Millisecond)
	for time.Now().Before(deadline) {
		if _, err := marker.WriteAt([]byte{0}, 0); err != nil {
			return time.Time{}
		}
		change, _, _, ok := execFileTimes(name)
		if !ok {
			return time.Time{}
		}
		if change.After(initial) {
			return change
		}
		time.Sleep(100 * time.Microsecond)
	}
	return time.Time{}
}
