package legacycall

import (
	"errors"
	"os"
)

// Recheck the opened object, not its pre-open inode: publication may atomically
// replace a regular record between selection and opening.
func openRetained(path string, limit int) (*os.File, error) {
	file, err := openRetainedFile(path)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err == nil && (!info.Mode().IsRegular() || info.Size() > int64(limit)) {
		err = errors.New("invalid retained file")
	}
	if err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}
