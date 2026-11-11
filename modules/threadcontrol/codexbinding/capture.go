package codexbinding

import (
	"context"
	"crypto/rand"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"strings"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

type invokingSettings struct{ model, effort string }

type invokingTurn struct {
	session, turn string
	items         map[string]invokingSettings
}

// CaptureInvocation binds host inference settings to the execution item before
// it is delivered. Responses and MCP capture the same immutable step settings;
// turn_context instead retains the initial turn, even after a live update.
// Source: codex-rs/core/src/turn_metadata.rs:57:99@c82708f85803d21fe9046af407107a1ee105b6e5.
func (d *Driver) CaptureInvocation(caller control.Identity, model, effort string) error {
	if caller.Item == "" || model == "" {
		return errors.New("execution has no invoking inference identity or model")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, err := d.control.Caller(caller); err != nil {
		return err
	}
	if d.invocations == nil {
		d.invocations = make(map[string]invokingTurn)
	}
	turn := d.invocations[caller.Thread]
	if turn.session != caller.Session || turn.turn != caller.Turn {
		// Old-turn callers cannot pass logical admission. Keep all items in the
		// active turn, including yielded cells using an earlier step's settings.
		turn = invokingTurn{session: caller.Session, turn: caller.Turn, items: make(map[string]invokingSettings)}
	}
	settings := invokingSettings{model: model, effort: effort}
	if previous, ok := turn.items[caller.Item]; ok && previous != settings {
		return errors.New("conflicting invoking inference settings for execution item")
	}
	turn.items[caller.Item] = settings
	d.invocations[caller.Thread] = turn
	return nil
}

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
	d.mu.RLock()
	turn, observed := d.invocations[caller.Thread]
	invoking, captured := turn.items[caller.Item]
	d.mu.RUnlock()
	if observed {
		if !captured || turn.session != caller.Session || turn.turn != caller.Turn {
			return PreparedChild{}, errors.New("host MCP call has no matching invoking inference")
		}
		// Inference metadata carries effective effort, including a model default
		// when the retained turn's configured effort is unset. Never replace
		// permissions, provider provenance or configuration from MCP arguments.
		settings.Model, settings.Effort = invoking.model, nil
		if invoking.effort != "" {
			settings.Effort = new(invoking.effort)
		}
	}
	if settings.Model != caller.Model || captured && invoking.effort != caller.Effort || caller.Effort != "" && (settings.Effort == nil || *settings.Effort != caller.Effort) {
		effort := "<unset>"
		if settings.Effort != nil {
			effort = *settings.Effort
		}
		return PreparedChild{}, fmt.Errorf("host MCP metadata and invoking inference settings disagree: MCP model=%q effort=%q; captured model=%q effort=%q", caller.Model, caller.Effort, settings.Model, effort)
	}
	d.mu.RLock()
	nativeModels := d.nativeModels
	d.mu.RUnlock()
	prepared, err := prepareChild(settings, member.Limits, options, models, config.Layers, [3]*string{config.Config.Agents.Model, config.Config.Agents.Effort, config.Config.Features.MultiAgent.ChildInstructions}, nativeModels)
	if errors.Is(err, errContextPreload) {
		d.control.RecordFeature(ctx, caller.Thread, rand.Text(), control.FeatureEvent{Kind: control.FeaturePreloadRejected})
	}
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
