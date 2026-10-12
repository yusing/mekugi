//go:build !unix

package sourceparse

import "os"

func Open(path string) (*os.File, error) {
	return os.Open(path)
}
