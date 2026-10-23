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

	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

type SpawnOptions struct {
	TaskName        string  `json:"task_name"`
	Message         string  `json:"message"`
	AgentType       *string `json:"agent_type,omitempty"`
	ForkTurns       *string `json:"fork_turns,omitempty"`
	Model           *string `json:"model,omitempty"`
	ReasoningEffort *string `json:"reasoning_effort,omitempty"`
	Cwd             *string `json:"cwd,omitempty"`
}

type Model struct {
	Name                  string
	Root, Child           *string
	DefaultEffort         *string
	Efforts, ServiceTiers []string
}

type PreparedChild struct {
	Import            string
	Arguments         jsontext.Value
	Settings          control.Settings
	Limits            control.Limits
	FullHistory       bool
	TaskName, Message string
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
	model, effort := options.Model, options.ReasoningEffort
	if !result.FullHistory {
		if model == nil {
			model = defaults[0]
		}
		if effort == nil {
			effort = defaults[1]
		}
	}
	if model != nil {
		i := slices.IndexFunc(models, func(m Model) bool { return m.Name == *model })
		if i < 0 {
			return result, fmt.Errorf("unknown agent model %q", *model)
		}
		settings.Model, settings.Effort = *model, models[i].DefaultEffort
	}
	if effort != nil {
		settings.Effort = effort
	}
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
	}
	if settings.Effort != nil && (model != nil || effort != nil || settings.Model != result.Settings.Model || !sameEffort(settings.Effort, result.Settings.Effort)) {
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
	result.Settings = settings
	result.Arguments, err = json.Marshal(&options, json.Deterministic(true))
	return result, err
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
