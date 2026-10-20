package router

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

func ensureWorkerSymlinkInDirectory(executable, directory, name string) (string, error) {
	link := filepath.Join(directory, name)
	if _, err := os.Lstat(link); err == nil {
		return verifyWorkerSymlink(link, executable, name)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect %s worker symlink: %w", name, err)
	}
	if err := os.Symlink(executable, link); err != nil {
		if errors.Is(err, os.ErrExist) {
			return verifyWorkerSymlink(link, executable, name)
		}
		return "", fmt.Errorf("create %s worker symlink: %w", name, err)
	}
	return link, nil
}

func verifyWorkerSymlink(link, executable, name string) (string, error) {
	target, err := os.Readlink(link)
	if err != nil {
		return "", fmt.Errorf("install %s worker: %s already exists and is not a symlink", name, link)
	}
	if target != executable {
		return "", fmt.Errorf("install %s worker: %s points to %s, want %s", name, link, target, executable)
	}
	return link, nil
}

func removeWorkerFrontendSymlink(link, wrapper string) error {
	target, err := os.Readlink(link)
	if errors.Is(err, os.ErrNotExist) || err == nil && target != wrapper {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect worker frontend %s: %w", link, err)
	}
	if err := os.Remove(link); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove worker frontend %s: %w", link, err)
	}
	return nil
}

func removeWorkerFrontendSymlinks(frontends, wrappers map[string]string) error {
	var cleanupErrors []error
	for name, link := range frontends {
		if err := removeWorkerFrontendSymlink(link, wrappers[name]); err != nil {
			cleanupErrors = append(cleanupErrors, err)
		}
	}
	return errors.Join(cleanupErrors...)
}
