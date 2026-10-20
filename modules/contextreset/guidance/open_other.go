//go:build !unix

package guidance

import (
	"errors"
	"os"
)

func openSource(string) (*os.File, error) {
	return nil, errors.New("link-safe guidance snapshots are unavailable on this platform")
}
