//go:build unix

package router

import (
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// Pin each directory without following links. This closes the replacement race
// after EvalSymlinks, including links in an ancestor rather than the final file.
func openJournalGuidanceFile(path string) (*os.File, error) {
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unix.Close(fd) }()
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for i, part := range parts {
		flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_NONBLOCK | unix.O_CLOEXEC
		if i < len(parts)-1 {
			flags |= unix.O_DIRECTORY
		}
		next, err := unix.Openat(fd, part, flags, 0)
		if err != nil {
			return nil, err
		}
		if i == len(parts)-1 {
			return os.NewFile(uintptr(next), path), nil
		}
		_ = unix.Close(fd)
		fd = next
	}
	return nil, os.ErrInvalid
}
