package codexbinding

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"strings"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

// PrepareSpawn captures configuration provenance and the exact invoking turn.
// Models is the invocation owner's actual catalog snapshot, not model/list's
// narrower display projection. No creation RPC runs until preparation succeeds.
func (d *Driver) PrepareSpawn(ctx context.Context, caller binding.MCPIdentity, options SpawnOptions, models []Model) (PreparedChild, error) {
	member, err := d.control.Caller(control.Identity{Thread: caller.Thread, Session: caller.Session, Turn: caller.Turn, Call: caller.Call, Item: caller.Item})
	if err != nil {
		return PreparedChild{}, err
	}
	settings, config, path, err := d.capture(ctx, control.Identity{Thread: caller.Thread, Session: caller.Session, Turn: caller.Turn})
	if err != nil {
		return PreparedChild{}, err
	}
	if settings.Model != caller.Model || caller.Effort != "" && (settings.Effort == nil || *settings.Effort != caller.Effort) {
		return PreparedChild{}, errors.New("host MCP metadata and invoking runtime settings disagree")
	}
	prepared, err := prepareChild(settings, member.Limits, options, models, config.Layers, [3]*string{config.Config.Agents.Model, config.Config.Agents.Effort, config.Config.Features.MultiAgent.ChildInstructions})
	if err != nil {
		return prepared, err
	}
	var overrides map[string]jsontext.Value
	if len(prepared.Settings.Config) > 0 {
		if err := json.Unmarshal(prepared.Settings.Config, &overrides); err != nil {
			return prepared, err
		}
	}
	if overrides == nil {
		overrides = make(map[string]jsontext.Value)
	}
	// Freeze only custom child hints in the root-shaped runtime. Instructions
	// selects generated child guidance from each request's current model catalog.
	delete(overrides, "features.multi_agent_v2.root_agent_usage_hint_text")
	if baseline := config.Config.Features.MultiAgent.ChildUsage; baseline != nil {
		overrides["features.multi_agent_v2.root_agent_usage_hint_text"], _ = json.Marshal(baseline)
	}
	prepared.Settings.Config, err = json.Marshal(overrides)
	if err == nil && prepared.FullHistory {
		if strings.HasPrefix(settings.Model, "claude:") {
			// Native session identity is sufficient for a Claude fork. The retained
			// import record carries a descriptor, never a Codex transcript.
			var source jsontext.Value
			source, err = json.Marshal(struct {
				Backend string `json:"backend"`
				Session string `json:"session"`
				Path    string `json:"path"`
			}{Backend: "claude", Session: caller.Session, Path: path})
			if err == nil {
				prepared.Import, err = d.control.PublishImport(ctx, control.Identity{Thread: caller.Thread, Session: caller.Session, Turn: caller.Turn, Call: caller.Call, Item: caller.Item}, source)
			}
			return prepared, err
		}
		source := binding.ForkSource{Thread: caller.Thread, Rollout: caller.Thread, Path: path}
		prefix, readErr := binding.ReadForkPrefix(ctx, source, binding.ForkBoundary{Turn: caller.Turn, Item: caller.Item}, func(ctx context.Context, thread string) (binding.ForkSource, error) {
			return d.forkSource(ctx, caller.Thread, thread)
		})
		if readErr != nil {
			return prepared, readErr
		}
		prefix, err = sanitizeFork(prefix)
		if err == nil {
			prepared.Import, err = d.control.PublishImport(ctx, control.Identity{Thread: caller.Thread, Session: caller.Session, Turn: caller.Turn, Call: caller.Call, Item: caller.Item}, prefix)
		}
	}
	return prepared, err
}
