package pathdisplay

import "path/filepath"

// ForWorkspace keeps authored relative paths unchanged, shortens absolute paths
// inside workspace, and preserves absolute paths outside it.
func ForWorkspace(workspace, path string) string {
	if path == "" || !filepath.IsAbs(path) {
		return path
	}
	if relative, err := filepath.Rel(workspace, path); err == nil && filepath.IsLocal(relative) {
		return relative
	}
	return path
}
