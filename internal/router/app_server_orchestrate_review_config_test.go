package router

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestWorkflowReviewerSettings(t *testing.T) {
	write := func(t *testing.T, path, text string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	layer := func(folder string, agents map[string]any) map[string]any {
		return map[string]any{"name": map[string]any{"type": "user", "file": filepath.Join(folder, "config.toml")}, "config": map[string]any{"agents": agents}}
	}
	resolve := func(t *testing.T, role string, layers ...map[string]any) (configuredRoleSettings, error) {
		t.Helper()
		raw, err := json.Marshal(map[string]any{"config": map[string]any{}, "origins": map[string]any{}, "layers": layers})
		if err != nil {
			t.Fatal(err)
		}
		return resolveConfiguredRoleSettings(raw, role)
	}
	check := func(t *testing.T, got configuredRoleSettings, err error, want configuredRoleSettings) {
		t.Helper()
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("settings = %#v, error = %v; want %#v", got, err, want)
		}
	}
	ptr := func(s string) *string { return &s }
	t.Run("absent and unrelated roles", func(t *testing.T) {
		dir := t.TempDir()
		write(t, filepath.Join(dir, "agents", "other.toml"), "name = 'other'\nmodel = 42\n")
		_, err := resolve(t, "audit-custom", layer(dir, map[string]any{"max_threads": 4, "other": map[string]any{"config_file": "missing.toml"}}))
		if err == nil {
			t.Fatal("missing selected role accepted")
		}
	})
	t.Run("discovered nested local shape and sorted duplicate precedence", func(t *testing.T) {
		dir := t.TempDir()
		write(t, filepath.Join(dir, "agents", "unrelated.toml"), "name = 'other'\nmodel = [")
		write(t, filepath.Join(dir, "agents", "a", "custom.toml"), "name = ' audit-custom '\ndescription = 'Review changes'\nnickname_candidates = [' Agent-1_2 ', 'Other Agent']\ndeveloper_instructions = 'Custom rubric'\nmodel = 'gpt-6-astra'\nmodel_reasoning_effort = 'high'\nservice_tier = 'priority'\nmodel_provider = 'ignored'\n[features]\nshell_tool = false\n")
		write(t, filepath.Join(dir, "agents", "z.toml"), "name = 'audit-custom'\ndescription = 'Duplicate'\nmodel = 'loser'\n")
		got, err := resolve(t, "audit-custom", layer(dir, nil))
		check(t, got, err, configuredRoleSettings{ptr("Custom rubric"), ptr("gpt-6-astra"), ptr("high"), ptr("priority")})
	})
	t.Run("explicit alternate file and file metadata precedence", func(t *testing.T) {
		dir := t.TempDir()
		write(t, filepath.Join(dir, "alternate.toml"), "name = 'reduce-custom'\ndescription = 'File description'\nmodel = 'chosen'\n")
		write(t, filepath.Join(dir, "agents", "reduce-custom.toml"), "name = 'reduce-custom'\ndescription = 'Discovered'\nmodel = 'ignored'\n")
		config := layer(dir, map[string]any{"alias": map[string]any{"config_file": "alternate.toml", "description": "Declaration"}})
		got, err := resolve(t, "reduce-custom", config)
		check(t, got, err, configuredRoleSettings{Model: ptr("chosen")})
	})
	t.Run("declared name hint and skip declared discovery file", func(t *testing.T) {
		dir := t.TempDir()
		write(t, filepath.Join(dir, "agents", "audit-custom.toml"), "service_tier = ''\n")
		got, err := resolve(t, "audit-custom", layer(dir, map[string]any{"audit-custom": map[string]any{"config_file": "agents/audit-custom.toml", "description": "Review"}}))
		check(t, got, err, configuredRoleSettings{ServiceTier: ptr("")})
	})
	t.Run("higher file replaces settings and metadata inherits", func(t *testing.T) {
		low, high, disabled := t.TempDir(), t.TempDir(), t.TempDir()
		write(t, filepath.Join(low, "agents", "audit-custom.toml"), "name = 'audit-custom'\ndescription = 'Inherited description'\nmodel = 'low'\nservice_tier = 'priority'\n")
		write(t, filepath.Join(high, "override.toml"), "model = 'high'\n")
		hi := layer(high, map[string]any{"audit-custom": map[string]any{"config_file": "override.toml"}})
		hi["name"] = map[string]any{"type": "project", "dotCodexFolder": high}
		off := layer(disabled, map[string]any{"audit-custom": map[string]any{"config_file": "missing.toml"}})
		off["disabledReason"] = "untrusted"
		off["config"] = map[string]any{"agents": "invalid disabled content"}
		got, err := resolve(t, "audit-custom", off, hi, layer(low, nil))
		check(t, got, err, configuredRoleSettings{Model: ptr("high")})
		// A metadata-only higher role keeps the lower config file.
		hi["config"] = map[string]any{"agents": map[string]any{"audit-custom": map[string]any{"description": "Updated"}}}
		got, err = resolve(t, "audit-custom", hi, layer(low, nil))
		check(t, got, err, configuredRoleSettings{Model: ptr("low"), ServiceTier: ptr("priority")})
	})
	t.Run("absolute session flag file and renamed declaration", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "alternate.toml")
		write(t, path, "name = 'reduce-custom'\ndescription = 'File metadata'\nmodel = 'chosen'\n")
		config := layer(dir, map[string]any{"audit-custom": map[string]any{"config_file": path}})
		config["name"] = map[string]any{"type": "sessionFlags"}
		got, err := resolve(t, "reduce-custom", config)
		check(t, got, err, configuredRoleSettings{Model: ptr("chosen")})
		_, err = resolve(t, "audit-custom", config)
		if err == nil {
			t.Fatal("renamed selected role accepted")
		}
	})
	t.Run("explicit empty overrides remain present", func(t *testing.T) {
		dir := t.TempDir()
		write(t, filepath.Join(dir, "role.toml"), "developer_instructions = ''\nmodel = ''\nmodel_reasoning_effort = ''\nservice_tier = ''\n")
		got, err := resolve(t, "reduce-custom", layer(dir, map[string]any{"reduce-custom": map[string]any{"config_file": "role.toml", "description": "Review"}}))
		check(t, got, err, configuredRoleSettings{ptr(""), ptr(""), ptr(""), ptr("")})
	})
	t.Run("requested errors are visible and sanitized", func(t *testing.T) {
		for _, contents := range []string{
			"model = 'secret'\nbroken = [", "model = ['secret']", "description = ''", "name = 42",
			"nickname_candidates = 42", "nickname_candidates = []", "nickname_candidates = ['secret', 1]",
			"nickname_candidates = [' ']", "nickname_candidates = [' secret ', 'secret']", "nickname_candidates = ['secret!']",
		} {
			dir := t.TempDir()
			write(t, filepath.Join(dir, "role.toml"), contents)
			_, err := resolve(t, "audit-custom", layer(dir, map[string]any{"audit-custom": map[string]any{"config_file": "role.toml", "description": "Review"}}))
			if err == nil || strings.Contains(err.Error(), "secret") {
				t.Fatalf("expected sanitized requested-role error, got %v", err)
			}
		}
		_, err := resolve(t, "audit-custom", layer(t.TempDir(), map[string]any{"audit-custom": map[string]any{"config_file": "missing.toml", "description": "Review"}}))
		if err == nil {
			t.Fatal("missing requested file was ignored")
		}
		dir := t.TempDir()
		write(t, filepath.Join(dir, "agents", "custom-name.toml"), "name = 'audit-custom'\nmodel = [")
		_, err = resolve(t, "audit-custom", layer(dir, nil))
		if err == nil {
			t.Fatal("malformed selected role accepted")
		}
		write(t, filepath.Join(dir, "agents", "custom-name.toml"), "name = 'audit-custom'\ndescription = 'Review'\nmodel = ['secret']")
		if _, err := resolve(t, "audit-custom", layer(dir, nil)); err == nil {
			t.Fatal("identified reviewer with invalid typed settings was ignored")
		}
	})
	t.Run("layer ownership is required", func(t *testing.T) {
		config := layer(t.TempDir(), map[string]any{"reduce-custom": map[string]any{"config_file": "relative.toml", "description": "Review"}})
		config["name"] = map[string]any{"type": "sessionFlags"}
		if _, err := resolve(t, "reduce-custom", config); err == nil {
			t.Fatal("relative file resolved without a layer folder")
		}
		for _, raw := range []jsontext.Value{[]byte(`{}`), []byte(`{"layers":null}`), []byte(`{"layers":`)} {
			if _, err := resolveConfiguredRoleSettings(raw, "audit-custom"); err == nil {
				t.Fatal("missing or invalid layers accepted")
			}
		}
	})
}

func loadWorkflowReviewerTestConfig(t *testing.T, body string) workflowReviewerRoles {
	t.Helper()
	directory := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", directory)
	if actual, _ := os.UserConfigDir(); actual != directory {
		t.Skip("platform does not use XDG_CONFIG_HOME")
	}
	if err := os.Mkdir(filepath.Join(directory, "mekugi"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "mekugi", "config.toml")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	config, err := loadMekugiConfig(false)
	if err != nil {
		t.Fatal(err)
	}
	return config.Orchestration.Reviewers
}

func TestWorkflowReviewerConfigDefaults(t *testing.T) {
	for _, body := range []string{"", "[orchestration.reviewers]\ncorrectness = ''\nsimplification = ''\n"} {
		if got := loadWorkflowReviewerTestConfig(t, body); got != (workflowReviewerRoles{}) {
			t.Fatal("unexpected default role mapping", got)
		}
	}
	// No mapping ignores even malformed host role metadata and needs no role files.
	got, err := resolveConfiguredRoleSettings([]byte(`{"layers":[{"config":{"agents":{"review-correctness":42,"review-simplify":42}}}]}`), "")
	if err != nil || got != (configuredRoleSettings{}) {
		t.Fatal("unselected roles affected defaults", got, err)
	}
}
