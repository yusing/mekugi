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

// CommonDirectory returns the shared directory prefix of already formatted
// paths, without cleaning or reordering them. Empty operands and literal brace
// notation stay ungrouped so a display abbreviation cannot obscure boundaries.
func CommonDirectory(paths []string) string {
	if len(paths) < 2 {
		return ""
	}
	prefix := paths[0]
	for _, path := range paths {
		if path == "" || strings.ContainsAny(path, "{},\r\n") {
			return ""
		}
		i := 0
		for i < min(len(prefix), len(path)) && prefix[i] == path[i] {
			i++
		}
		prefix = prefix[:i]
	}
	prefix = prefix[:strings.LastIndexByte(prefix, filepath.Separator)+1]
	for _, path := range paths {
		if path == prefix {
			return ""
		}
	}
	return prefix
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
