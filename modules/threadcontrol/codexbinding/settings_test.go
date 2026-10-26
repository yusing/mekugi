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
