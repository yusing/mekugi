package executionobservation

import (
	"encoding/json"
	"os"
	"path/filepath"
)

func (p *Preparer) observationDirectory(declared map[string]json.RawMessage, thread string) string {
	if directory, valid := usableRoutingDirectory(declared); valid {
		return directory
	}
	// An admitted workspace is durable identity, even after worktree removal.
	// Explicit command directories still establish their own capture targets.
	workspace := p.review.WorkspaceForThread(thread)
	_, matches := declared[workspace]
	if workspace != "" && (len(declared) == 0 || matches) {
		return workspace
	}
	return ""
}

func usableRoutingDirectory(declared map[string]json.RawMessage) (string, bool) {
	for path := range declared {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			continue
		}
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			continue
		}
		canonical, err := filepath.EvalSymlinks(path)
		if err != nil || !filepath.IsAbs(canonical) {
			continue
		}
		return canonical, true
	}
	return "", false
}
