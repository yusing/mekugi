package pathdisplay

import (
	"path/filepath"
	"strings"
)

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

// Move shows both workspace-relative endpoints without repeating shared folders.
func Move(workspace, before, after string) string {
	before, after = ForWorkspace(workspace, before), ForWorkspace(workspace, after)
	prefix := ""
	for i := 0; i < min(len(before), len(after)) && before[i] == after[i]; i++ {
		if before[i] == filepath.Separator {
			prefix = before[:i+1]
		}
	}
	if prefix == "" {
		return before + " => " + after
	}
	return prefix + "{" + strings.TrimPrefix(before, prefix) + "=>" + strings.TrimPrefix(after, prefix) + "}"
}
