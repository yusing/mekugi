// Source: apps/mekugi/internal/router/app_server_file_picker.go:157:166@[e9da429a] VCS metadata exclusion.
package composer

import (
	"path/filepath"
	"strings"
)

// Ignore-rule bypass never exposes version-control implementation metadata.
func VCSPath(path string) bool {
	for part := range strings.SplitSeq(filepath.ToSlash(path), "/") {
		switch part {
		case ".git", ".svn", ".hg", ".bzr", "_darcs", "CVS":
			return true
		}
	}
	return false
}
