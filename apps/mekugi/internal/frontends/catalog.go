// Source: apps/mekugi/internal/router/tool_registry_types.go:1:48@[7b6d9ccc]
package frontends

import "sync"

type (
	// Registry is one authenticated catalog and its invocation-owned resources.
	Registry struct {
		SnapshotDir       string
		ReplayDirectory   string
		FrontendDirectory string

		closeOnce sync.Once
		closeErr  error
	}

	workerManifest struct {
		ReplayDirectory string   `json:"replay_directory,omitempty"`
		Version         int      `json:"version"`
		RegistryID      string   `json:"registry_id"`
		Tools           []string `json:"tools"`
	}
)
