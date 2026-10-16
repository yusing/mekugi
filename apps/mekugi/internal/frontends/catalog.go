// Source: apps/mekugi/internal/router/tool_registry_types.go:1:48@[7b6d9ccc]
package frontends

import (
	"encoding/json"
	"sync"

	"github.com/yusing/mekugi/backends/codex/binding"
)

type (
	catalogEntry struct {
		Name           string          `json:"name"`
		Specification  json.RawMessage `json:"specification"`
		NativeExecutor string          `json:"native_executor,omitempty"`
		Executable     bool            `json:"executable"`
	}

	// Registry is one authenticated catalog and its invocation-owned resources.
	Registry struct {
		SnapshotDir       string
		ReplayDirectory   string
		FrontendDirectory string
		ordered           []catalogEntry
		FrontendGuidance  string
		Guidance          *binding.Guidance
		wrappers          map[string]string
		Frontends         map[string]string

		closeOnce sync.Once
		closeErr  error
	}

	workerManifest struct {
		ReplayDirectory string         `json:"replay_directory,omitempty"`
		AXReadOutput    string         `json:"ax_read_output,omitempty"`
		Version         int            `json:"version"`
		RegistryID      string         `json:"registry_id"`
		GuidanceSHA256  string         `json:"guidance_sha256"`
		Tools           []catalogEntry `json:"tools"`
	}
)
