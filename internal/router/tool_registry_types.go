package router

import (
	"encoding/json"
	"sync"
)

type (
	toolContribution struct {
		PluginID      string          `json:"plugin_id"`
		Name          string          `json:"name"`
		Specification json.RawMessage `json:"specification"`
		Module        string          `json:"module,omitempty"`
		ModuleIndex   int             `json:"module_index,omitempty"`
		Builtin       bool            `json:"builtin"`
		Executable    bool            `json:"executable"`
	}

	toolRegistry struct {
		SnapshotDir       string
		RuntimeRoot       string
		NodeExecutable    string
		frontendDirectory string
		runtimeDirectory  string
		ordered           []toolContribution
		byName            map[string]toolContribution
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
		NodeExecutable  string             `json:"node_executable,omitempty"`
		RuntimeRoot     string             `json:"runtime_root"`
		Tools           []toolContribution `json:"tools"`
	}
)
