package execscope

import (
	"os"
	"path/filepath"
)

// execBuiltinPruned keeps formatter and fixer scopes out of dependency trees,
// bytecode caches, tagged caches, and virtual environments.
func execBuiltinPruned(path, name string) bool {
	if name == "node_modules" || name == "__pycache__" {
		return true
	}
	for _, marker := range []string{"CACHEDIR.TAG", "pyvenv.cfg"} {
		if _, err := os.Lstat(filepath.Join(path, marker)); err == nil {
			return true
		}
	}
	return false
}
