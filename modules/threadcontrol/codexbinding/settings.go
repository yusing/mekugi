package codexbinding

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/yusing/mekugi/modules/agentguidance/compact"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

type SpawnOptions struct {
	TaskName        string         `json:"task_name"`
	Message         string         `json:"message"`
	AgentType       *string        `json:"agent_type,omitempty"`
	ForkTurns       *string        `json:"fork_turns,omitempty"`
	Model           *string        `json:"model,omitempty"`
	ReasoningEffort *string        `json:"reasoning_effort,omitempty"`
	Cwd             *string        `json:"cwd,omitempty"`
	Preload         jsontext.Value `json:"preload,omitempty"`
}

type Model struct {
	Name                  string
	Root, Child           *string
	DefaultEffort         *string
	MultiAgentEffort      *string
	Efforts, ServiceTiers []string
}

type PreparedChild struct {
	Import            string
	Arguments         jsontext.Value
	Settings          control.Settings
	Limits            control.Limits
	FullHistory       bool
	TaskName, Message string
	Preload           string
}

// prepareChild translates the selected invocation's values, leaving execution
// and final settings validation with the independent native runtime.
func prepareChild(settings control.Settings, limits control.Limits, options SpawnOptions, models []Model, layers []ConfigLayer, defaults [3]*string) (PreparedChild, error) {
	result := PreparedChild{Settings: settings, Limits: limits, TaskName: options.TaskName, Message: options.Message, FullHistory: true}
	if strings.TrimSpace(options.Message) == "" {
		return result, errors.New("empty agent assignment")
	}
	if options.ForkTurns != nil {
		switch *options.ForkTurns {
		case "all", "":
		case "none":
			result.FullHistory = false
		default:
			return result, errors.New("fork_turns must be all or none")
		}
	}
	if result.FullHistory && (options.Model != nil || options.ReasoningEffort != nil) {
		return result, errors.New("full-history forks inherit model and effort; use fork_turns=none for overrides")
	}
	if len(options.Preload) > 0 {
		if result.FullHistory {
			return result, errors.New("context preload requires fork_turns=none")
		}
		if _, err := compact.Sources(string(options.Preload)); err != nil {
			return result, fmt.Errorf("context preload: %w", err)
		}
		result.Preload = string(options.Preload)
	}
	model, effort := options.Model, options.ReasoningEffort
	claude := strings.HasPrefix(settings.Model, "claude:")
	if model != nil {
		claude = strings.HasPrefix(*model, "claude:")
	}
	if claude && effort != nil {
		return result, fmt.Errorf("unsupported reasoning effort %q for Claude delegates", *effort)
	}
	// Source: codex-rs/core/src/agent/child_config.rs:52:76,204:244@0df4aa677.
	// Configured subagent defaults precede roles, including full-history forks.
	if model == nil && !claude {
		model = defaults[0]
	}
	if effort == nil && !claude {
		effort = defaults[1]
	}
	if claude {
		if model != nil {
			settings.Model = *model
		}
		if strings.TrimSpace(strings.TrimPrefix(settings.Model, "claude:")) == "" {
			return result, errors.New("Claude model must not be empty")
		}
		settings.Effort, settings.Summary, settings.Tier = nil, nil, nil
	} else if model != nil {
		i := slices.IndexFunc(models, func(m Model) bool { return m.Name == *model })
		if i < 0 {
			return result, fmt.Errorf("unknown agent model %q", *model)
		}
		settings.Model, settings.Effort = *model, models[i].DefaultEffort
	}
	if effort != nil {
		settings.Effort = effort
	}
	inheritedEffort := !claude && model == nil && effort == nil
	if defaults[2] != nil {
		settings.DeveloperInstructions = defaults[2]
	}
	role := ""
	if options.AgentType != nil {
		role = strings.TrimSpace(*options.AgentType)
	}
	if role != "" || !result.FullHistory {
		if role == "" {
			role = "default"
		}
		roles, err := roleCatalog(layers)
		if err != nil {
			return result, err
		}
		selection, found := roles[role]
		if !found {
			return result, fmt.Errorf("unknown agent_type %q", role)
		}
		if err := applyRole(&settings, selection); err != nil {
			return result, err
		}
		inheritedEffort = inheritedEffort && selection.values.Effort == nil
	}
	if inheritedEffort && settings.Effort != nil && *settings.Effort == "ultra" {
		ordinary, err := inheritedEffortForModel(models, settings.Model, *settings.Effort)
		if err != nil {
			return result, err
		}
		settings.Effort = new(ordinary)
	} else if settings.Effort != nil && (model != nil || effort != nil || settings.Model != result.Settings.Model || !sameEffort(settings.Effort, result.Settings.Effort)) {
		i := slices.IndexFunc(models, func(m Model) bool { return m.Name == settings.Model })
		if i < 0 || !slices.Contains(models[i].Efforts, *settings.Effort) {
			return result, fmt.Errorf("unsupported reasoning effort %q for %s", *settings.Effort, settings.Model)
		}
	}
	// Session tier wins over role configuration, but only when the final model
	// supports it. The default sentinel explicitly clears the provider override.
	if tier := settings.Tier; tier != nil && *tier != "default" && *tier != "flex" {
		i := slices.IndexFunc(models, func(m Model) bool { return m.Name == settings.Model })
		if i < 0 || !slices.Contains(models[i].ServiceTiers, *tier) {
			settings.Tier = nil
		}
	}
	if options.Cwd != nil {
		settings.Workspace = *options.Cwd
		if !filepath.IsAbs(settings.Workspace) {
			settings.Workspace = filepath.Join(result.Settings.Workspace, settings.Workspace)
		}
		var err error
		settings.Workspace, err = filepath.EvalSymlinks(settings.Workspace)
		if err != nil {
			return result, err
		}
	}
	info, err := os.Stat(settings.Workspace)
	if err != nil {
		return result, err
	}
	if !filepath.IsAbs(settings.Workspace) || !info.IsDir() {
		return result, errors.New("agent cwd must be an existing absolute directory")
	}
	settings.NonAttachable = strings.HasPrefix(settings.Model, "claude:")
	result.Settings = settings
	result.Arguments, err = json.Marshal(&options, json.Deterministic(true))
	return result, err
}

func inheritedEffortForModel(models []Model, model, effort string) (string, error) {
	if effort != "ultra" {
		return effort, nil
	}
	i := slices.IndexFunc(models, func(m Model) bool { return m.Name == model })
	if i < 0 {
		return "", fmt.Errorf("inherited ultra requires the selected model catalog for %s", model)
	}
	return models[i].ordinaryUltraEffort(), nil
}

// Source: codex-rs/protocol/src/openai_models/reasoning_effort.rs:10:36@0df4aa677.
// Store the ordinary effort so inherited ultra does not activate proactive mode.
func (m Model) ordinaryUltraEffort() string {
	if m.MultiAgentEffort != nil && *m.MultiAgentEffort != "ultra" && slices.Contains(m.Efforts, *m.MultiAgentEffort) {
		return *m.MultiAgentEffort
	}
	if slices.Contains(m.Efforts, "max") {
		return "max"
	}
	for _, effort := range slices.Backward(m.Efforts) {
		if effort != "ultra" {
			return effort
		}
	}
	return "medium"
}

// config/read includes absent TOML options as null. Omit those values when
// freezing the effective table for an invocation-local config override.
func PermissionConfig(raw jsontext.Value) (jsontext.Value, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	var prune func(any)
	prune = func(value any) {
		switch v := value.(type) {
		case map[string]any:
			for key, item := range v {
				if item == nil {
					delete(v, key)
				} else {
					prune(item)
				}
			}
		case []any:
			for _, item := range v {
				prune(item)
			}
		}
	}
	prune(value)
	return json.Marshal(value)
}

func sameEffort(left, right *string) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}
