package codexbinding

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func TestPrepareChildModelEffortAndContext(t *testing.T) {
	workspace := t.TempDir()
	parent := control.Settings{Workspace: workspace, Model: "parent", Effort: new("high"), Provider: "provider", Tier: new("priority"), PermissionProfile: []byte(`{"type":"disabled"}`), ApprovalPolicy: []byte(`"never"`)}
	models := []Model{{Name: "parent", ServiceTiers: []string{"priority"}}, {Name: "other", DefaultEffort: new("medium"), Efforts: []string{"medium", "low"}}}
	for _, tc := range []struct {
		name, model, effort string
		options             SpawnOptions
		defaults            [3]*string
		full, fail          bool
	}{
		{name: "full history inherits", model: "parent", effort: "high", full: true},
		{name: "full history configured defaults", model: "other", effort: "low", full: true, defaults: [3]*string{new("other"), new("low"), nil}},
		{name: "fresh inherits", model: "parent", effort: "high", options: SpawnOptions{ForkTurns: new("none")}},
		{name: "fresh defaults", model: "other", effort: "low", options: SpawnOptions{ForkTurns: new("none")}, defaults: [3]*string{new("other"), new("low"), nil}},
		{name: "fresh explicit model default effort", model: "other", effort: "medium", options: SpawnOptions{ForkTurns: new("none"), Model: new("other")}},
		{name: "explicit settings precede configured defaults", model: "other", effort: "medium", options: SpawnOptions{ForkTurns: new("none"), Model: new("other"), ReasoningEffort: new("medium")}, defaults: [3]*string{new("parent"), new("low"), nil}},
		{name: "full model override refused", options: SpawnOptions{Model: new("other")}, fail: true},
		{name: "full effort override refused", options: SpawnOptions{ReasoningEffort: new("low")}, fail: true},
		{name: "full preload refused", options: SpawnOptions{Preload: []byte(`{"sources":[]}`)}, fail: true},
		{name: "fresh preload", model: "parent", effort: "high", options: SpawnOptions{ForkTurns: new("none"), Preload: []byte(`{"sources":[{"path":"context.go","ranges":[[2,4]]},{"skill":"review"}]}`)}},
		{name: "malformed preload refused", options: SpawnOptions{ForkTurns: new("none"), Preload: []byte(`{"source":[]}`)}, fail: true},
		{name: "null preload refused", options: SpawnOptions{ForkTurns: new("none"), Preload: []byte(`null`)}, fail: true},
		{name: "unsupported effort refused", options: SpawnOptions{ForkTurns: new("none"), Model: new("other"), ReasoningEffort: new("high")}, fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.options.Message = "assignment"
			child, err := prepareChild(parent, control.Limits{}, tc.options, models, nil, tc.defaults)
			if tc.fail {
				if err == nil {
					t.Fatal("invalid override admitted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if child.FullHistory != tc.full || child.Settings.Model != tc.model || child.Settings.Effort == nil || *child.Settings.Effort != tc.effort {
				t.Fatalf("preparation = %+v", child)
			}
			if child.Preload != string(tc.options.Preload) {
				t.Fatal("preload selection was not frozen")
			}
			if child.Settings.Provider != parent.Provider || !reflect.DeepEqual(child.Settings.PermissionProfile, parent.PermissionProfile) || !reflect.DeepEqual(child.Settings.ApprovalPolicy, parent.ApprovalPolicy) {
				t.Fatalf("parent execution settings lost: %+v", child.Settings)
			}
			wantTier := parent.Tier
			if tc.model == "other" {
				wantTier = nil
			}
			if !reflect.DeepEqual(child.Settings.Tier, wantTier) {
				t.Fatalf("tier = %v, want %v", child.Settings.Tier, wantTier)
			}
		})
	}
}

func TestPrepareChildRelativeCwdUsesSelectedWorkspace(t *testing.T) {
	workspace := t.TempDir()
	childDir := filepath.Join(workspace, "child")
	if err := os.Mkdir(childDir, 0700); err != nil {
		t.Fatal(err)
	}
	child, err := prepareChild(control.Settings{Workspace: workspace, Model: "parent"}, control.Limits{}, SpawnOptions{Message: "assignment", Cwd: new("child")}, nil, nil, [3]*string{})
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(childDir)
	if err != nil {
		t.Fatal(err)
	}
	if child.Settings.Workspace != want {
		t.Fatalf("cwd = %q, want %q", child.Settings.Workspace, want)
	}
}

func TestPrepareChildInheritedUltra(t *testing.T) {
	driver := &Driver{}
	driver.CaptureCatalog([]byte(`{"models":[
		{"slug":"metadata","multi_agent_reasoning_effort":"high","supported_reasoning_levels":[{"effort":"medium"},{"effort":"high"},{"effort":"max"},{"effort":"ultra"}]},
		{"slug":"max","multi_agent_reasoning_effort":"unknown","supported_reasoning_levels":[{"effort":"max"},{"effort":"ultra"},{"effort":"high"}]},
		{"slug":"ordered","multi_agent_reasoning_effort":"ultra","supported_reasoning_levels":[{"effort":"high"},{"effort":"custom"},{"effort":"ultra"}]},
		{"slug":"empty"},
		{"slug":"default-ultra","default_reasoning_level":"ultra","supported_reasoning_levels":[{"effort":"max"},{"effort":"ultra"}]}
	]}`))
	if driver.catalogError != nil {
		t.Fatal(driver.catalogError)
	}
	for _, tc := range []struct {
		name, model, want, role string
		options                 SpawnOptions
		defaults                [3]*string
	}{
		{name: "full inherits model equivalent", model: "metadata", want: "high"},
		{name: "fresh inherits model equivalent", model: "metadata", want: "high", options: SpawnOptions{ForkTurns: new("none")}},
		{name: "invalid metadata uses max", model: "max", want: "max"},
		{name: "ultra metadata uses last ordinary", model: "ordered", want: "custom"},
		{name: "empty levels stock fallback", model: "empty", want: "medium"},
		{name: "explicit ultra preserved", model: "metadata", want: "ultra", options: SpawnOptions{ForkTurns: new("none"), ReasoningEffort: new("ultra")}},
		{name: "configured ultra preserved", model: "metadata", want: "ultra", defaults: [3]*string{nil, new("ultra"), nil}},
		{name: "fixed role ultra preserved", model: "metadata", want: "ultra", role: "model_reasoning_effort = 'ultra'"},
		{name: "role without effort still inherits", model: "metadata", want: "high", role: "developer_instructions = 'bounded worker'"},
		{name: "role overrides explicit effort", model: "metadata", want: "medium", role: "model_reasoning_effort = 'medium'", options: SpawnOptions{ForkTurns: new("none"), ReasoningEffort: new("ultra")}},
		{name: "role selected model equivalent", model: "metadata", want: "max", role: "model = 'max'"},
		{name: "model default preserved", model: "metadata", want: "ultra", options: SpawnOptions{ForkTurns: new("none"), Model: new("default-ultra")}},
		{name: "missing catalog rejected", model: "missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace := t.TempDir()
			parent := control.Settings{Workspace: workspace, Model: tc.model, Effort: new("ultra"), Config: []byte(`{"features.multi_agent_v2.multi_agent_mode_hint_text":"CUSTOM_MODE"}`)}
			var layers []ConfigLayer
			if tc.role != "" {
				roleTestFile(t, filepath.Join(workspace, "worker.toml"), tc.role)
				layers = []ConfigLayer{roleTestLayer(t, workspace, map[string]any{"agents": map[string]any{"worker": map[string]string{"config_file": "worker.toml"}}})}
				tc.options.AgentType = new("worker")
			}
			tc.options.Message = "assignment"
			child, err := prepareChild(parent, control.Limits{}, tc.options, driver.models, layers, tc.defaults)
			if tc.want == "" {
				if err == nil {
					t.Fatal("missing normalization catalog accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if child.Settings.Effort == nil || *child.Settings.Effort != tc.want || *parent.Effort != "ultra" {
				t.Fatalf("child effort = %v, want %s; parent = %s", child.Settings.Effort, tc.want, *parent.Effort)
			}
			if string(child.Settings.Config) != string(parent.Config) {
				t.Fatalf("custom mode changed: %s", child.Settings.Config)
			}
		})
	}
}
