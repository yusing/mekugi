package runtimepath

import (
	"fmt"
	"os"
	"path/filepath"
)

const DirectoryEnvironment = "MEKUGI_RUNTIME_DIR"

func Directory() (string, error) {
	directory := os.Getenv(DirectoryEnvironment)
	if directory == "" {
		directory = os.TempDir()
	}
	if !filepath.IsAbs(directory) {
		return "", fmt.Errorf("%s must be an absolute path", DirectoryEnvironment)
	}
	return filepath.Clean(directory), nil
}
