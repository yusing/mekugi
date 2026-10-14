package router

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
)

type configuredRoleSettings struct {
	DeveloperInstructions *string
	Model                 *string
	ReasoningEffort       *string
	ServiceTier           *string
}

type workflowReviewerRole struct {
	file        string
	description *string
	values      map[string]any
}

// Source: codex-rs/agent-roles/src/loader.rs:23:117@ddb33e34b load_agent_roles
// Source: codex-rs/app-server/src/config_manager_service.rs:162:177@ddb33e34b
// config/read supplies high-to-low layers for the SOURCE cwd. Resolve role
// metadata first; a higher role file replaces, rather than merges, its settings.
// Only the four requested overrides cross this adapter. In particular, an
// explicit role tier wins here even though native role application resets it.
func resolveConfiguredRoleSettings(raw jsontext.Value, role string) (configuredRoleSettings, error) {
	var settings configuredRoleSettings
	if role == "" {
		return settings, nil
	}
	var result struct {
		Layers []struct {
			Name struct {
				Type           string `json:"type"`
				File           string `json:"file"`
				DotCodexFolder string `json:"dotCodexFolder"`
			} `json:"name"`
			Config         jsontext.Value `json:"config"`
			DisabledReason *string        `json:"disabledReason"`
		} `json:"layers"`
	}
	if json.Unmarshal(raw, &result) != nil || result.Layers == nil {
		return settings, errors.New("agent role settings require config/read with includeLayers")
	}
	var selected workflowReviewerRole
	var discoveryErr error
	for _, layer := range slices.Backward(result.Layers) {
		if layer.DisabledReason != nil {
			continue
		}
		var config struct {
			Agents map[string]jsontext.Value `json:"agents"`
		}
		if json.Unmarshal(layer.Config, &config) != nil {
			return settings, errors.New("agent role config layer has malformed agents metadata")
		}
		// Source: codex-rs/config/src/state.rs:219:233@ddb33e34b config_folder
		var folder string
		switch layer.Name.Type {
		case "system", "user":
			if !filepath.IsAbs(layer.Name.File) {
				return settings, errors.New("agent role config layer requires an absolute file")
			}
			folder = filepath.Dir(layer.Name.File)
		case "project":
			folder = layer.Name.DotCodexFolder
			if !filepath.IsAbs(folder) {
				return settings, errors.New("agent role config layer requires an absolute folder")
			}
		}
		var current *workflowReviewerRole
		declared := make(map[string]bool)
		for _, name := range slices.Sorted(maps.Keys(config.Agents)) {
			var entry struct {
				File        *string `json:"config_file"`
				Description *string `json:"description"`
			}
			if json.Unmarshal(config.Agents[name], &entry) != nil {
				if name == role {
					return settings, fmt.Errorf("agent role %s has malformed role metadata", role)
				}
				continue
			}
			candidate := workflowReviewerRole{description: entry.Description}
			resolved := name
			if entry.File != nil {
				path := *entry.File
				if !filepath.IsAbs(path) && folder != "" {
					path = filepath.Join(folder, path)
				}
				var err error
				resolved, candidate, err = readWorkflowReviewerRole(path, name, role)
				if err != nil {
					return settings, err
				}
				if candidate.description == nil {
					candidate.description = entry.Description
				}
				if candidate.file != "" {
					declared[candidate.file] = true
				}
			}
			if resolved == role && current == nil {
				current = &candidate
			}
		}
		if folder != "" {
			// Source: codex-rs/agent-roles/src/discovery.rs:7:49@ddb33e34b
			// Native discovery sorts the full paths after recursive collection.
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
				return settings, fmt.Errorf("agent role %s agents directory is unreadable", role)
			}
			slices.Sort(files)
			for _, path := range files {
				name, candidate, err := readWorkflowReviewerRole(path, "", role)
				if err != nil {
					// Source: codex-rs/agent-roles/src/loader.rs:302:315@ddb33e34b
					// Invalid discovered files cannot shadow a valid role.
					if discoveryErr == nil {
						discoveryErr = err
					}
					continue
				}
				if name == role && current == nil {
					current = &candidate
				}
			}
		}
		if current != nil {
			if current.description == nil {
				current.description = selected.description
			}
			if current.description == nil || strings.TrimSpace(*current.description) == "" {
				return settings, fmt.Errorf("agent role %s requires a non-empty description", role)
			}
			if current.file == "" {
				current.file, current.values = selected.file, selected.values
			}
			selected = *current
		}
	}
	if selected.description == nil {
		if discoveryErr != nil {
			return settings, discoveryErr
		}
		return settings, fmt.Errorf("agent role %s is unavailable", role)
	}
	for _, field := range []struct {
		key string
		dst **string
	}{
		{"developer_instructions", &settings.DeveloperInstructions},
		{"model", &settings.Model},
		{"model_reasoning_effort", &settings.ReasoningEffort},
		{"service_tier", &settings.ServiceTier},
	} {
		if value, exists := selected.values[field.key]; exists {
			text, ok := value.(string)
			if !ok {
				return configuredRoleSettings{}, fmt.Errorf("agent role %s field %s must be a string", role, field.key)
			}
			*field.dst = &text
		}
	}
	return settings, nil
}

// Source: codex-rs/agent-roles/src/agent_role_config.rs:39:124@ddb33e34b
// File name metadata wins over a declared name; discovered files need name
// metadata. As in native discovery, skip malformed definitions until the
// requested role is identified. Explicit requested declarations fail visibly.
func readWorkflowReviewerRole(path, hint, requested string) (string, workflowReviewerRole, error) {
	var candidate workflowReviewerRole
	relevant := hint == requested
	fail := func(reason string) (string, workflowReviewerRole, error) {
		if relevant {
			return "", candidate, fmt.Errorf("agent role %s file %q %s", requested, path, reason)
		}
		return "", candidate, nil
	}
	if !filepath.IsAbs(path) {
		return fail("requires an absolute path or a config layer folder")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return fail("is unreadable or is not a regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fail("is unreadable")
	}
	var values map[string]any
	if _, err := toml.Decode(string(data), &values); err != nil {
		// Parser diagnostics can contain source text and secrets.
		return fail("contains malformed TOML")
	}
	name := hint
	if value, exists := values["name"]; exists {
		text, ok := value.(string)
		if !ok {
			return fail("has invalid name metadata")
		}
		if strings.TrimSpace(text) != "" {
			name = strings.TrimSpace(text)
		}
	}
	if name == "" {
		return fail("requires name metadata")
	}
	if name != requested {
		return name, workflowReviewerRole{file: path}, nil
	}
	relevant = true
	// Source: codex-rs/agent-roles/src/agent_role_config.rs:133:158@ddb33e34b
	// Discovered roles require instructions; declared roles may inherit them.
	if value, exists := values["developer_instructions"]; exists {
		text, ok := value.(string)
		if !ok || strings.TrimSpace(text) == "" {
			return fail("has invalid developer_instructions")
		}
	} else if hint == "" {
		return fail("requires developer_instructions")
	}
	if value, exists := values["description"]; exists {
		text, ok := value.(string)
		if !ok || strings.TrimSpace(text) == "" {
			return fail("has invalid description metadata")
		}
		candidate.description = &text
	}
	// Nickname metadata is consumed by Codex's role parser even though this
	// adapter only forwards the four execution overrides.
	if value, exists := values["nickname_candidates"]; exists {
		nicknames, ok := value.([]any)
		if !ok || len(nicknames) == 0 {
			return fail("has invalid nickname_candidates metadata")
		}
		seen := make(map[string]bool)
		for _, value := range nicknames {
			name, ok := value.(string)
			name = strings.TrimSpace(name)
			if !ok || name == "" || seen[name] || strings.IndexFunc(name, func(r rune) bool {
				return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == ' ' || r == '-' || r == '_')
			}) >= 0 {
				return fail("has invalid nickname_candidates metadata")
			}
			seen[name] = true
		}
	}
	candidate.file, candidate.values = path, values
	return name, candidate, nil
}

func (settings configuredRoleSettings) apply(params map[string]any) {
	if settings.DeveloperInstructions != nil {
		params["developerInstructions"] = *settings.DeveloperInstructions
	}
	if settings.Model != nil {
		params["model"] = *settings.Model
	}
	if settings.ReasoningEffort != nil {
		params["config"].(map[string]any)["model_reasoning_effort"] = *settings.ReasoningEffort
	}
	if settings.ServiceTier != nil {
		params["serviceTier"] = nil
		if *settings.ServiceTier != "" {
			params["serviceTier"] = *settings.ServiceTier
		}
	}
}
