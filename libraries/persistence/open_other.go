//go:build !unix && !windows && !wasip1

package persistence

import (
	"errors"
	"os"
)

func openRetainedFile(path string) (*os.File, error) {
	// Without a no-follow opener, accept only a descriptor matching a regular
	// pathname observation. Either side of an atomic regular-file publication
	// is valid; a transient symlink cannot introduce an unobserved object.
	// This fallback cannot make a special-file replacement's open nonblocking.
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, errors.New("invalid retained file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	opened, err := file.Stat()
	if err == nil {
		var after os.FileInfo
		after, err = os.Lstat(path)
		if err == nil && (!after.Mode().IsRegular() || !os.SameFile(opened, before) && !os.SameFile(opened, after)) {
			err = errors.New("retained file changed during opening")
		}
	}
	if err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}
