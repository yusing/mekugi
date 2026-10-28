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

func TestPrepareChildClaudeSelectionAndRole(t *testing.T) {
	workspace := t.TempDir()
	roleTestFile(t, filepath.Join(workspace, "worker.toml"), `model = 'codex-pinned'
model_reasoning_effort = 'ultra'
model_reasoning_summary = 'detailed'
service_tier = 'priority'
developer_instructions = 'Review the assignment'
[features]
shell_tool = false
plugins = true
`)
	layers := []ConfigLayer{roleTestLayer(t, workspace, map[string]any{"agents": map[string]any{"worker": map[string]string{"config_file": "worker.toml"}, "default": map[string]string{"config_file": "worker.toml"}}})}
	models := []Model{{Name: "codex", DefaultEffort: new("medium"), Efforts: []string{"medium"}}, {Name: "codex-pinned", Efforts: []string{"ultra"}, ServiceTiers: []string{"priority"}}}
	for _, tc := range []struct {
		name, parent, want string
		options            SpawnOptions
		fail               bool
	}{
		{name: "explicit backend survives role pins", parent: "codex", want: "claude:unlisted-model", options: SpawnOptions{ForkTurns: new("none"), Model: new("claude:unlisted-model"), AgentType: new("worker")}},
		{name: "fresh Claude inherits with default role", parent: "claude:parent", want: "claude:parent", options: SpawnOptions{ForkTurns: new("none")}},
		{name: "full Claude inherits despite selected role", parent: "claude:parent", want: "claude:parent", options: SpawnOptions{AgentType: new("worker")}},
		{name: "full Claude inherits without role", parent: "claude:parent", want: "claude:parent"},
		{name: "explicit Codex restores stock role precedence", parent: "claude:parent", want: "codex-pinned", options: SpawnOptions{ForkTurns: new("none"), Model: new("codex"), ReasoningEffort: new("medium"), AgentType: new("worker")}},
		{name: "empty qualified model", parent: "codex", options: SpawnOptions{ForkTurns: new("none"), Model: new("claude:")}, fail: true},
		{name: "blank qualified model", parent: "codex", options: SpawnOptions{ForkTurns: new("none"), Model: new("claude: \t")}, fail: true},
		{name: "explicit Claude effort", parent: "codex", options: SpawnOptions{ForkTurns: new("none"), Model: new("claude:model"), ReasoningEffort: new("high")}, fail: true},
		{name: "inherited Claude explicit effort", parent: "claude:parent", options: SpawnOptions{ForkTurns: new("none"), ReasoningEffort: new("")}, fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent := control.Settings{Workspace: workspace, Model: tc.parent, Provider: "codex-provider", Effort: new("ultra"), Summary: new("auto"), Tier: new("priority"), PermissionProfile: []byte(`{"type":"managed","file_system":{"type":"restricted","entries":[{"path":{"type":"path","path":"/denied"},"access":"deny"}]},"network":"restricted"}`), ApprovalPolicy: []byte(`"on-request"`), PermissionID: "bounded", ApprovalsReviewer: "user", RuntimeRoots: []string{workspace}, Config: []byte(`{"features.plugins":false}`), NonAttachable: tc.parent != "codex"}
			tc.options.Message = "assignment"
			child, err := prepareChild(parent, control.Limits{}, tc.options, models, layers, [3]*string{new("codex"), new("medium"), new("default instructions")})
			if tc.fail {
				if err == nil {
					t.Fatal("unsupported Claude selection admitted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if child.Settings.Model != tc.want || child.Settings.Provider != parent.Provider || !reflect.DeepEqual(child.Settings.PermissionProfile, parent.PermissionProfile) || !reflect.DeepEqual(child.Settings.ApprovalPolicy, parent.ApprovalPolicy) {
				t.Fatalf("child selection or authority = %+v", child.Settings)
			}
			if child.Settings.NonAttachable != (tc.want != "codex-pinned") || child.Settings.PermissionID != parent.PermissionID || child.Settings.ApprovalsReviewer != parent.ApprovalsReviewer || !reflect.DeepEqual(child.Settings.RuntimeRoots, parent.RuntimeRoots) {
				t.Fatalf("child capabilities or authority = %+v", child.Settings)
			}
			if tc.want != "codex-pinned" && (child.Settings.Effort != nil || child.Settings.Summary != nil || child.Settings.Tier != nil) {
				t.Fatalf("Claude inherited Codex settings: %+v", child.Settings)
			}
			if tc.options.AgentType != nil || !child.FullHistory {
				if child.Settings.DeveloperInstructions == nil || *child.Settings.DeveloperInstructions != "Review the assignment" || child.Settings.SelectedRole == nil {
					t.Fatalf("role instructions lost: %+v", child.Settings)
				}
				assertForkJSON(t, child.Settings.Config, `{"features.plugins":false,"features.shell_tool":false}`)
			}
		})
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
