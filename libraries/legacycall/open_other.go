//go:build !unix

package legacycall

import "os"

func openRetainedFile(path string) (*os.File, error) {
	// Preserve the platform opener; descriptor validation is shared. Unix-only
	// nonblocking and no-follow flags have no portable os.Open equivalent.
	return os.Open(path)
}
