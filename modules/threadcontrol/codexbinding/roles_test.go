package codexbinding

import (
	"crypto/sha256"
	json "encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func roleTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
}

func roleTestLayer(t *testing.T, folder string, config any) ConfigLayer {
	t.Helper()
	layer := ConfigLayer{}
	layer.Name.Type, layer.Name.DotCodexFolder = "project", folder
	var err error
	layer.Config, err = json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	return layer
}

func TestRoleLayerPrecedenceInPreparation(t *testing.T) {
	workspace := t.TempDir()
	low, high := filepath.Join(workspace, "low"), filepath.Join(workspace, "high")
	roleTestFile(t, filepath.Join(low, "worker.toml"), "model = 'low'\ndeveloper_instructions = 'low role'\n")
	contents := "model = 'high'\ndeveloper_instructions = 'high role'\n"
	path := filepath.Join(high, "worker.toml")
	roleTestFile(t, path, contents)
	lower := roleTestLayer(t, low, map[string]any{"agents": map[string]any{"worker": map[string]any{"config_file": "worker.toml", "description": "lower"}}})
	for _, tc := range []struct {
		name, model, path, contents string
		entry                       map[string]any
	}{
		{"higher file replaces builtin and lower file", "high", path, contents, map[string]any{"config_file": "worker.toml"}},
		{"description inherits lower file", "low", filepath.Join(low, "worker.toml"), "model = 'low'\ndeveloper_instructions = 'low role'\n", map[string]any{"description": "higher"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upper := roleTestLayer(t, high, map[string]any{"agents": map[string]any{"worker": tc.entry}})
			// Host config/read orders higher-priority layers first.
			child, err := prepareChild(control.Settings{Workspace: workspace, Model: "parent"}, control.Limits{}, SpawnOptions{Message: "task", AgentType: new("worker"), ForkTurns: new("none")}, nil, []ConfigLayer{upper, lower}, [3]*string{})
			if err != nil {
				t.Fatal(err)
			}
			role := child.Settings.SelectedRole
			if child.Settings.Model != tc.model || role == nil || role.Path != tc.path || role.Contents != tc.contents || role.Digest != fmt.Sprintf("%x", sha256.Sum256([]byte(tc.contents))) {
				t.Fatalf("selected settings = %+v, role = %+v", child.Settings, role)
			}
			wantDescription := "lower"
			if tc.entry["description"] != nil {
				wantDescription = "higher"
			}
			if role.Description != wantDescription {
				t.Fatalf("description = %q, want %q", role.Description, wantDescription)
			}
		})
	}
}

func TestRoleDiscoveredPreparationFreezesSourceAndReductions(t *testing.T) {
	workspace := t.TempDir()
	folder := filepath.Join(workspace, ".codex")
	path := filepath.Join(folder, "agents", "nested", "review.toml")
	contents := `name = "auditor"
description = "Review assigned code"
developer_instructions = "Check the assignment"
[features]
shell_tool = true
apps = false
memory_tool = false
[skills]
include_instructions = true
[skills.bundled]
enabled = false
[[skills.config]]
path = "../disabled-skill"
enabled = false
[[skills.config]]
path = "../enabled-skill"
enabled = true
`
	roleTestFile(t, path, contents)
	parent := control.Settings{Workspace: workspace, Model: "parent", Config: []byte(`{"features.shell_tool":false,"features.plugins":false,"skills.include_instructions":false,"skills.config":[{"path":"/inherited/skill","enabled":false}]}`)}
	child, err := prepareChild(parent, control.Limits{}, SpawnOptions{Message: "task", AgentType: new("auditor"), ForkTurns: new("none")}, nil, []ConfigLayer{roleTestLayer(t, folder, map[string]any{})}, [3]*string{})
	if err != nil {
		t.Fatal(err)
	}
	assertForkJSON(t, child.Settings.Config, fmt.Sprintf(`{"features.shell_tool":false,"features.plugins":false,"features.apps":false,"features.memories":false,"skills.include_instructions":false,"skills.bundled.enabled":false,"skills.config":[{"path":"/inherited/skill","enabled":false},{"path":%q,"enabled":false}]}`, filepath.Join(filepath.Dir(path), "../disabled-skill")))
	roleTestFile(t, path, "name = 'auditor'\ndeveloper_instructions = 'Changed after admission'\n")
	// Durable preparation must survive serialization without consulting the file.
	data, err := json.Marshal(&child.Settings)
	if err != nil {
		t.Fatal(err)
	}
	var restored control.Settings
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.Role != "auditor" || restored.DeveloperInstructions == nil || *restored.DeveloperInstructions != "Check the assignment" || restored.SelectedRole == nil || restored.SelectedRole.Path != path || restored.SelectedRole.Contents != contents {
		t.Fatalf("restored preparation lost frozen role: %+v", restored)
	}
	assertForkJSON(t, restored.Config, string(child.Settings.Config))
	assertForkJSON(t, parent.Config, `{"features.shell_tool":false,"features.plugins":false,"skills.include_instructions":false,"skills.config":[{"path":"/inherited/skill","enabled":false}]}`)
}

func TestRoleInlineSkillReductions(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "role.toml")
	roleTestFile(t, path, "[skills]\nconfig = [{ path = 'disabled', enabled = false }, { path = 'enabled', enabled = true }]\n")
	layer := roleTestLayer(t, workspace, map[string]any{"agents": map[string]any{"worker": map[string]any{"config_file": "role.toml"}}})
	child, err := prepareChild(control.Settings{Workspace: workspace, Config: []byte(`{"skills.config":[{"name":"inherited","enabled":false}]}`)}, control.Limits{}, SpawnOptions{Message: "task", AgentType: new("worker")}, nil, []ConfigLayer{layer}, [3]*string{})
	if err != nil {
		t.Fatal(err)
	}
	assertForkJSON(t, child.Settings.Config, fmt.Sprintf(`{"skills.config":[{"name":"inherited","enabled":false},{"path":%q,"enabled":false}]}`, filepath.Join(workspace, "disabled")))
}

func TestRoleConsumedFieldsValidateBeforeAdmission(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "role.toml")
	layer := roleTestLayer(t, workspace, map[string]any{"agents": map[string]any{"worker": map[string]any{"config_file": "role.toml"}}})
	for _, contents := range []string{
		"model = false", "developer_instructions = 4", "model_reasoning_effort = true", "model_reasoning_summary = 4", "service_tier = false",
		"[features]\napps = 'disabled'", "[skills]\ninclude_instructions = 'disabled'", "[skills]\nconfig = [{path = 'skill'}]",
	} {
		t.Run(contents, func(t *testing.T) {
			roleTestFile(t, path, contents)
			if _, err := prepareChild(control.Settings{Workspace: workspace}, control.Limits{}, SpawnOptions{Message: "task", AgentType: new("worker")}, nil, []ConfigLayer{layer}, [3]*string{}); err == nil {
				t.Fatal("invalid role configuration was admitted")
			}
		})
	}
}

func TestRoleServiceTierPrecedenceAndFinalModel(t *testing.T) {
	workspace := t.TempDir()
	roleTestFile(t, filepath.Join(workspace, "role.toml"), "model = 'selected'\nservice_tier = 'priority'\n")
	layer := roleTestLayer(t, workspace, map[string]any{"agents": map[string]any{"worker": map[string]any{"config_file": "role.toml"}}})
	for _, tc := range []struct {
		name      string
		inherited *string
		supported bool
		want      *string
	}{
		{"role fills absent session tier", nil, true, new("priority")},
		{"session tier wins", new("flex"), true, new("flex")},
		{"session default wins", new("default"), true, new("default")},
		{"final model clears unsupported role tier", nil, false, nil},
		{"unsupported session tier does not fall back to role", new("unsupported"), true, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := Model{Name: "selected"}
			if tc.supported {
				model.ServiceTiers = []string{"priority"}
			}
			child, err := prepareChild(control.Settings{Workspace: workspace, Model: "parent", Tier: tc.inherited}, control.Limits{}, SpawnOptions{Message: "task", AgentType: new("worker")}, []Model{model}, []ConfigLayer{layer}, [3]*string{})
			if err != nil {
				t.Fatal(err)
			}
			if child.Settings.Model != "selected" || !reflect.DeepEqual(child.Settings.Tier, tc.want) {
				t.Fatalf("prepared settings = %+v, want tier %v", child.Settings, tc.want)
			}
		})
	}
}
