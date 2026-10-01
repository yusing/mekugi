//go:build unix

package router

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// openInventoryFile pins every directory without following symlinks. The final
// symlink is read as a link, never opened. Special files are opened nonblocking
// and rejected by the caller before reading.
func openInventoryFile(root, relative string) (*os.File, string, error) {
	if !filepath.IsLocal(relative) {
		return nil, "", errors.New("inventory path is outside its workspace")
	}
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, "", err
	}
	parts := strings.Split(filepath.Clean(relative), string(filepath.Separator))
	for _, part := range parts[:len(parts)-1] {
		next, err := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if errors.Is(err, os.ErrNotExist) {
			return nil, "", err
		}
		if err != nil {
			return nil, "", errors.New("inventory ancestor is unavailable or no longer a non-symlink directory")
		}
		fd = next
	}
	defer unix.Close(fd)
	name := parts[len(parts)-1]
	opened, err := unix.Openat(fd, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ELOOP) {
		data := make([]byte, 4096)
		n, readErr := unix.Readlinkat(fd, name, data)
		if readErr != nil {
			return nil, "", readErr
		}
		if n == len(data) {
			return nil, "", errors.New("symlink target exceeds capture bound")
		}
		return nil, string(data[:n]), nil
	}
	if err != nil {
		return nil, "", err
	}
	return os.NewFile(uintptr(opened), filepath.Join(root, relative)), "", nil
}
