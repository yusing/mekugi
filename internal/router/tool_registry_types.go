package router

import (
	"encoding/json"
	"sync"

	"github.com/yusing/mekugi/modules/agentmetrics/evidence"

	"github.com/yusing/mekugi/backends/codex/binding"

	"github.com/yusing/mekugi/modules/outputrecovery/recovery"

	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

type (
	toolContribution struct {
		Name           string          `json:"name"`
		Specification  json.RawMessage `json:"specification"`
		NativeExecutor string          `json:"native_executor,omitempty"`
		Executable     bool            `json:"executable"`
	}

	toolRegistry struct {
		SnapshotDir       string
		frontendDirectory string
		ordered           []toolContribution
		frontendGuidance  string
		guidance          *binding.Guidance
		wrappers          map[string]string
		frontends         map[string]string

		closeOnce sync.Once
		closeErr  error
	}

	toolWorkerManifest struct {
		agentMetrics    *evidence.Service
		recovery        *recovery.Service
		history         *history.Service
		ReplayDirectory string             `json:"replay_directory,omitempty"`
		AXReadOutput    string             `json:"ax_read_output,omitempty"`
		Version         int                `json:"version"`
		RegistryID      string             `json:"registry_id"`
		GuidanceSHA256  string             `json:"guidance_sha256"`
		Tools           []toolContribution `json:"tools"`
	}
)
