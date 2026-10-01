//go:build !unix

package router

import (
	"errors"
	"os"
)

func openInventoryFile(root, relative string) (*os.File, string, error) {
	return nil, "", errors.New("no-follow inventory reads unavailable on this platform")
}
