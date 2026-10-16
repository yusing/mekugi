package router

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const toolWorkerExecutableFilename = ".mekugi-worker"

func pinRunningToolWorker(directory string) error {
	return pinRunningExecutable(filepath.Join(directory, toolWorkerExecutableFilename))
}

func pinRunningExecutable(target string) (err error) {
	source, err := openRunningExecutable()
	if err != nil {
		return fmt.Errorf("open running executable: %w", err)
	}
	defer func() { err = errors.Join(err, source.Close()) }()
	location, _ := os.Executable()
	return pinToolWorkerExecutable(source, location, target)
}

// Pin the running inode, not a mutable installation pathname. A hard link avoids
// copying the binary on ordinary same-filesystem installations. Verify its inode
// against the open executable to catch replacement racing with registry startup.
// Cross-filesystem snapshots and unlinked executables use a private copy instead.
func pinToolWorkerExecutable(source *os.File, location, target string) error {
	info, err := source.Stat()
	if err != nil {
		return err
	}
	if err := os.Link(location, target); err == nil {
		linked, statErr := os.Lstat(target)
		if statErr == nil && linked.Mode().IsRegular() && os.SameFile(info, linked) {
			return nil
		}
		if err := os.Remove(target); err != nil {
			return err
		}
	}
	file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o500)
	if err != nil {
		return fmt.Errorf("pin tool worker executable: %w", err)
	}
	_, copyErr := io.Copy(file, source)
	return errors.Join(copyErr, file.Close())
}
