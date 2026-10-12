package sourceparse

import "path/filepath"

func Path(cwd, path string) string {
	if path == "" {
		return ""
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	if !filepath.IsAbs(cwd) {
		return ""
	}
	return filepath.Join(cwd, path)
}
