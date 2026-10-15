package router

import (
	"encoding/json"
	"sync"
)

type (
	toolContribution struct {
		Name           string          `json:"name"`
		Specification  json.RawMessage `json:"specification"`
		NativeExecutor string          `json:"native_executor,omitempty"`
		Builtin        bool            `json:"builtin"`
		Executable     bool            `json:"executable"`
	}

	toolRegistry struct {
		SnapshotDir       string
		frontendDirectory string
		runtimeDirectory  string
		ordered           []toolContribution
		frontendGuidance  string
		wrappers          map[string]string
		frontends         map[string]string
		diagnoseHooks     diagnoseHooks
		diagnoseEnabled   bool

		closeOnce sync.Once
		closeErr  error
	}

	toolWorkerManifest struct {
		HookDirectory   string             `json:"hook_directory,omitempty"`
		ReplayDirectory string             `json:"replay_directory,omitempty"`
		AXReadOutput    string             `json:"ax_read_output,omitempty"`
		Version         int                `json:"version"`
		RegistryID      string             `json:"registry_id"`
		GuidanceSHA256  string             `json:"guidance_sha256"`
		Tools           []toolContribution `json:"tools"`
	}
)
