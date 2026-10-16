//go:build !linux

// Source: apps/mekugi/internal/router/worker_executable_other.go:1:13@[7b6d9ccc]
package frontends

import "os"

func openRunningExecutable() (*os.File, error) {
	path, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return os.Open(path)
}
