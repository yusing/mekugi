package executionobservation

import (
	"encoding/json"
	"os"
	"path/filepath"
)

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
