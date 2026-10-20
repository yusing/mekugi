//go:build !linux

package router

import "os"

func openRunningExecutable() (*os.File, error) {
	path, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return os.Open(path)
}
