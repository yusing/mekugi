package codexbinding

import (
	"crypto/sha256"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/yusing/mekugi/libraries/sourceanalysis/sourceparse"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

type ConfigLayer struct {
	Name struct {
		Type           string `json:"type"`
		File           string `json:"file"`
		DotCodexFolder string `json:"dotCodexFolder"`
	} `json:"name"`
	Config         jsontext.Value `json:"config"`
	DisabledReason *string        `json:"disabledReason"`
}

// RoleConfig is the supported role-file schema. It is decoded once and shared
// by discovery, preparation and the model-visible catalog.
type RoleConfig struct {
	Name        *string `toml:"name" json:"name,omitzero"`
	Description *string `toml:"description" json:"description,omitzero"`
	Model       *string `toml:"model" json:"model,omitzero"`
	Personality *string `toml:"personality" json:"personality,omitzero"`
	Developer   *string `toml:"developer_instructions" json:"developer_instructions,omitzero"`
	Effort      *string `toml:"model_reasoning_effort" json:"model_reasoning_effort,omitzero"`
	Summary     *string `toml:"model_reasoning_summary" json:"model_reasoning_summary,omitzero"`
	Verbosity   *string `toml:"model_verbosity" json:"model_verbosity,omitzero"`
	Tier        *string `toml:"service_tier" json:"service_tier,omitzero"`
	Features    struct {
		Shell       *bool `toml:"shell_tool" json:"shell_tool,omitzero"`
		Apps        *bool `toml:"apps" json:"apps,omitzero"`
		Plugins     *bool `toml:"plugins" json:"plugins,omitzero"`
		Memories    *bool `toml:"memories" json:"memories,omitzero"`
		Memory      *bool `toml:"memory_tool" json:"memory_tool,omitzero"`
		Permissions *bool `toml:"request_permissions_tool" json:"request_permissions_tool,omitzero"`
	} `toml:"features" json:"features"`
	Skills struct {
		Instructions *bool `toml:"include_instructions" json:"include_instructions,omitzero"`
		Bundled      struct {
			Enabled *bool `toml:"enabled" json:"enabled,omitzero"`
		} `toml:"bundled" json:"bundled"`
		Config []roleSkill `toml:"config" json:"config,omitzero"`
	} `toml:"skills" json:"skills"`
}

type roleSkill struct {
	Name    *string                   `toml:"name" json:"name,omitzero"`
	Path    *string                   `toml:"path" json:"path,omitzero"`
	Enabled *bool                     `toml:"enabled" json:"enabled,omitzero"`
	Other   map[string]jsontext.Value `toml:"-" json:",embed"`
}

type roleDefinition struct {
	selection control.RoleSelection
	values    RoleConfig
	err       error
}

// One admitted catalog supplies both model-visible role choices and selection.
// Higher layers replace files, while description-only declarations inherit them.
func roleCatalog(layers []ConfigLayer) (map[string]roleDefinition, error) {
	roles := make(map[string]roleDefinition)
	for name, description := range map[string]string{"default": "General-purpose agent.", "explorer": "Investigate a bounded codebase question.", "worker": "Carry out an explicitly assigned implementation task."} {
		roles[name] = roleDefinition{selection: control.RoleSelection{Name: name, Description: description}}
	}
	for _, layer := range slices.Backward(layers) {
		if layer.DisabledReason != nil {
			continue
		}
		folder := layer.Name.DotCodexFolder
		if layer.Name.File != "" {
			folder = filepath.Dir(layer.Name.File)
		}
		if folder != "" && !filepath.IsAbs(folder) {
			return nil, errors.New("role layer has no absolute configuration directory")
		}
		var config struct {
			Agents map[string]jsontext.Value `json:"agents"`
		}
		if err := json.Unmarshal(layer.Config, &config); err != nil {
			return nil, err
		}
		selected := make(map[string]roleDefinition)
		declared := make(map[string]bool)
		for _, key := range slices.Sorted(maps.Keys(config.Agents)) {
			var entry struct {
				File        *string `json:"config_file"`
				Description *string `json:"description"`
			}
			if json.Unmarshal(config.Agents[key], &entry) != nil {
				continue
			}
			role := roleDefinition{selection: control.RoleSelection{Name: key}}
			if entry.Description != nil {
				role.selection.Description = strings.TrimSpace(*entry.Description)
				if role.selection.Description == "" {
					role.err = errors.New("agent role description must not be blank")
				}
			}
			if entry.File != nil && role.err == nil {
				path := *entry.File
				if !filepath.IsAbs(path) {
					path = filepath.Join(folder, path)
				}
				role.selection, role.values, role.err = readRole(path, role.selection)
				declared[path] = true
			}
			if _, exists := selected[role.selection.Name]; !exists {
				selected[role.selection.Name] = role
			}
		}
		if folder != "" {
			var files []string
			err := filepath.WalkDir(filepath.Join(folder, "agents"), func(path string, entry fs.DirEntry, err error) error {
				if errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				if err != nil {
					return err
				}
				if !entry.IsDir() && filepath.Ext(path) == ".toml" && !declared[path] {
					files = append(files, path)
				}
				return nil
			})
			if err != nil {
				return nil, err
			}
			slices.Sort(files)
			for _, path := range files {
				selection, values, err := readRole(path, control.RoleSelection{})
				if _, exists := selected[selection.Name]; err == nil && !exists {
					selected[selection.Name] = roleDefinition{selection: selection, values: values}
				}
			}
		}
		for name, role := range selected {
			prior := roles[name]
			if role.selection.Description == "" {
				role.selection.Description = prior.selection.Description
			}
			if role.selection.Path == "" && role.err == nil {
				role.selection.Path, role.selection.Contents, role.selection.Digest = prior.selection.Path, prior.selection.Contents, prior.selection.Digest
				role.values, role.err = prior.values, prior.err
			}
			if role.selection.Description == "" && role.err == nil {
				role.err = errors.New("agent role requires a description")
			}
			roles[name] = role
		}
	}
	return roles, nil
}

func readRole(path string, role control.RoleSelection) (control.RoleSelection, RoleConfig, error) {
	var values RoleConfig
	if !filepath.IsAbs(path) {
		return role, values, errors.New("relative role path has no configuration directory")
	}
	file, err := sourceparse.Open(path)
	if err != nil {
		return role, values, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return role, values, err
	}
	if !info.Mode().IsRegular() {
		return role, values, errors.New("agent role must be a regular file")
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return role, values, err
	}
	if _, err := toml.Decode(string(data), &values); err != nil {
		return role, values, err
	}
	for _, skill := range values.Skills.Config {
		if skill.Enabled == nil {
			return role, values, errors.New("role skill configuration requires enabled")
		}
	}
	if role.Name == "" && values.Developer == nil || values.Developer != nil && strings.TrimSpace(*values.Developer) == "" {
		return role, values, errors.New("agent role requires nonblank developer instructions")
	}
	for target, source := range map[*string]*string{&role.Name: values.Name, &role.Description: values.Description} {
		if source != nil {
			*target = strings.TrimSpace(*source)
			if *target == "" {
				return role, values, errors.New("agent role name and description must not be blank")
			}
		}
	}
	if role.Name == "" {
		return role, values, errors.New("agent role requires a name")
	}
	role.Path, role.Contents, role.Digest = path, string(data), fmt.Sprintf("%x", sha256.Sum256(data))
	return role, values, nil
}

func applyRole(settings *control.Settings, role roleDefinition) error {
	if role.err != nil {
		return role.err
	}
	var overrides struct {
		Disabled []roleSkill               `json:"skills.config,omitzero"`
		Config   map[string]jsontext.Value `json:",embed"`
	}
	if len(settings.Config) != 0 {
		if err := json.Unmarshal(settings.Config, &overrides); err != nil {
			return err
		}
	}
	if overrides.Config == nil {
		overrides.Config = make(map[string]jsontext.Value)
	}
	values := role.values
	if values.Developer != nil {
		settings.DeveloperInstructions = values.Developer
	}
	// Role authority and instructions apply across backends, but Codex model
	// settings cannot replace the selected native Claude backend.
	if !strings.HasPrefix(settings.Model, "claude:") {
		if settings.Tier == nil {
			settings.Tier = values.Tier
		}
		for target, value := range map[**string]*string{&settings.Effort: values.Effort, &settings.Summary: values.Summary} {
			if value != nil {
				*target = value
			}
		}
		if values.Model != nil {
			settings.Model = *values.Model
		}
	}
	for key, value := range map[string]*string{"model_verbosity": values.Verbosity, "personality": values.Personality} {
		if value != nil {
			overrides.Config[key], _ = json.Marshal(value)
		}
	}
	features := values.Features
	if features.Memory != nil && !*features.Memory {
		features.Memories = new(false)
	}
	for key, value := range map[string]*bool{
		"features.shell_tool": features.Shell, "features.apps": features.Apps,
		"features.plugins": features.Plugins, "features.memories": features.Memories,
		"features.request_permissions_tool": features.Permissions,
		"skills.include_instructions":       values.Skills.Instructions, "skills.bundled.enabled": values.Skills.Bundled.Enabled,
	} {
		if value != nil && !*value {
			overrides.Config[key] = jsontext.Value(`false`)
		}
	}
	for _, skill := range values.Skills.Config {
		if *skill.Enabled {
			continue
		}
		if skill.Path != nil && !filepath.IsAbs(*skill.Path) {
			skill.Path = new(filepath.Join(filepath.Dir(role.selection.Path), *skill.Path))
		}
		overrides.Disabled = append(overrides.Disabled, skill)
	}
	var err error
	settings.Config, err = json.Marshal(&overrides)
	settings.Role, settings.SelectedRole = role.selection.Name, &role.selection
	return err
}
