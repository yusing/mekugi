//go:build !unix

package capture

// Source: apps/mekugi/internal/router/exec_file_identity_other.go:1:9@1e6d58c6.

import "os"

func execFileIdentity(os.FileInfo) string {
	return ""
}
