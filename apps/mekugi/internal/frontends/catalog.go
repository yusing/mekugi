// Source: apps/mekugi/internal/router/tool_registry_types.go:1:48@[7b6d9ccc]
package frontends

import (
	"sync"

	"github.com/yusing/mekugi/backends/codex/binding"
)

type (
	// Registry is one authenticated catalog and its invocation-owned resources.
	Registry struct {
		SnapshotDir       string
		ReplayDirectory   string
		FrontendDirectory string
		FrontendGuidance  binding.FrontendGuidance
		Guidance          *binding.Guidance

		closeOnce sync.Once
		closeErr  error
	}

	workerManifest struct {
		ReplayDirectory string   `json:"replay_directory,omitempty"`
		AXReadOutput    string   `json:"ax_read_output,omitempty"`
		Version         int      `json:"version"`
		RegistryID      string   `json:"registry_id"`
		GuidanceSHA256  string   `json:"guidance_sha256"`
		Tools           []string `json:"tools"`
	}
)
