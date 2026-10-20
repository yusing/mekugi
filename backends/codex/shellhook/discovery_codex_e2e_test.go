//go:build codex_e2e

package shellhook

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yusing/mekugi/backends/codex/appserver"
)

func TestGuardDiscoveryConfigurationNativeCodex(t *testing.T) {
	binary, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal("installed Codex is required for native hook configuration acceptance")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	configRoot, workspace := t.TempDir(), t.TempDir()
	var providerCalls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalls.Add(1)
		http.Error(w, "this fixture starts no model turn", http.StatusServiceUnavailable)
	}))
	defer provider.Close()
	marker := filepath.Join(workspace, "hook-must-not-run")
	// Every handler would leave evidence if discovery accidentally began a thread.
	handler := fmt.Sprintf(`{type="command",command="touch '%s'",timeout=5,async=false}`, marker)
	configPath := filepath.Join(configRoot, "config.toml")
	callerFile := fmt.Sprintf(`check_for_update_on_startup = false
model_provider = "fixture"
[model_providers.fixture]
name = "No inference"
base_url = %q
wire_api = "responses"
requires_openai_auth = false
[features]
hooks = true
plugins = false
[hooks]
PreToolUse = [{matcher="Bash",hooks=[%s]}]
PostToolUse = [{matcher="Read",hooks=[%s]}]
`, provider.URL, handler, handler)
	writeConfig := func(contents string) {
		t.Helper()
		if err := os.WriteFile(configPath, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeConfig(callerFile)
	sessionGroups := `hooks.PreToolUse=[{matcher="Read",hooks=[]},{matcher="Bash",hooks=[` + handler + `,` + strings.Replace(handler, "async=false", "async=true", 1) + `]} ,{matcher="Read",hooks=[` + handler + `]}]`
	recovery := `hooks.SessionStart=[{matcher="compact",hooks=[` + handler + `]}]`
	environment := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + configRoot, "CODEX_HOME=" + configRoot, "XDG_CONFIG_HOME=" + configRoot, "XDG_STATE_HOME=" + configRoot}
	command := func(overrides ...string) *exec.Cmd {
		args := []string{"app-server", "-c", sessionGroups, "-c", recovery}
		for _, override := range overrides {
			args = append(args, "-c", override)
		}
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Dir, cmd.Env = workspace, environment
		return cmd
	}
	version := exec.CommandContext(ctx, binary, "--version")
	version.Env = environment
	if output, err := version.CombinedOutput(); err != nil {
		t.Fatalf("Codex version: %v: %s", err, output)
	} else {
		t.Log(strings.TrimSpace(string(output)))
	}
	// Learn trust identities from the host, then give each caller hook explicit
	// trust in its original layer. No guard-builder hash is used for this setup.
	_, untrusted := nativeHookCatalog(t, ctx, command())
	if len(untrusted) != 6 {
		t.Fatalf("initial caller hooks=%#v", untrusted)
	}
	fileState, sessionState := map[string]jsontext.Value{}, map[string]jsontext.Value{}
	for key, entry := range untrusted {
		state, err := json.Marshal(map[string]any{"trusted_hash": entry.Hash, "enabled": true})
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(key, configPath+":") {
			fileState[key] = state
		} else if strings.HasPrefix(key, "/<session-flags>/config.toml:") {
			sessionState[key] = state
		} else {
			t.Fatalf("unexpected caller hook source: %s", key)
		}
	}
	if len(fileState) != 2 || len(sessionState) != 4 {
		t.Fatalf("caller layer counts: file=%d session=%d", len(fileState), len(sessionState))
	}
	encodeState := func(state map[string]jsontext.Value) string {
		t.Helper()
		data, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		value, err := inlineTOML(data)
		if err != nil {
			t.Fatal(err)
		}
		return "hooks.state=" + value
	}
	writeConfig(callerFile + strings.TrimPrefix(encodeState(fileState), "hooks.") + "\n")
	original, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	sessionOverride := encodeState(sessionState)
	_, baseline := nativeHookCatalog(t, ctx, command(sessionOverride))
	for key, entry := range baseline {
		if !entry.Enabled || entry.Trust != "trusted" || entry.Hash != untrusted[key].Hash {
			t.Fatalf("caller hook was not trusted and enabled: %#v", entry)
		}
	}
	prior, disabled, err := Discover(ctx, command(sessionOverride))
	if err != nil {
		t.Fatal(err)
	}
	wantDisabled := map[string]bool{}
	for key, entry := range baseline {
		if entry.Event == "preToolUse" && entry.Matcher == "Bash" && !entry.Async {
			wantDisabled[key] = true
		}
	}
	if len(wantDisabled) != 2 || len(disabled) != 2 {
		t.Fatalf("conflict keys=%v want=%v", disabled, wantDisabled)
	}
	for _, key := range disabled {
		if !wantDisabled[key] {
			t.Fatalf("disabled unrelated hook %q", key)
		}
	}
	if len(prior.PreToolUse) != 3 {
		t.Fatalf("session groups=%s", prior.PreToolUse)
	}
	config, state, hash, err := Config(filepath.Join(workspace, "unused-helper"), filepath.Join(workspace, "guard"), "", "", prior, disabled)
	if err != nil {
		t.Fatal(err)
	}
	raw, guarded := nativeHookCatalog(t, ctx, command(sessionOverride, config, state))
	if err := Validate(raw, workspace, hash); err != nil {
		t.Fatalf("installed host rejected effective guard: %v\n%s", err, raw)
	}
	guardKey := "/<session-flags>/config.toml:pre_tool_use:3:0"
	guard, ok := guarded[guardKey]
	if !ok || !guard.Enabled || guard.Trust != "trusted" || guard.Hash != hash {
		t.Fatalf("appended guard metadata=%#v", guard)
	}
	if len(guarded) != len(baseline)+1 {
		t.Fatalf("guarded hook count=%d baseline=%d", len(guarded), len(baseline))
	}
	for key, entry := range baseline {
		expected := entry
		if wantDisabled[key] {
			expected.Enabled = false
		}
		if actual, ok := guarded[key]; !ok || actual != expected {
			t.Fatalf("changed caller hook %q: got %#v want %#v", key, actual, expected)
		}
	}
	_, restored := nativeHookCatalog(t, ctx, command(sessionOverride))
	if !reflect.DeepEqual(restored, baseline) {
		t.Fatalf("fresh invocation did not restore caller hooks: got %#v want %#v", restored, baseline)
	}
	current, err := os.ReadFile(configPath)
	if err != nil || !bytes.Equal(current, original) {
		t.Fatalf("caller config changed: %v", err)
	}
	if providerCalls.Load() != 0 {
		t.Fatalf("preflight made %d provider requests", providerCalls.Load())
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("configuration probe executed a hook: %v", err)
	}
	if _, err := os.Stat(filepath.Join(configRoot, "sessions")); !os.IsNotExist(err) {
		t.Fatalf("configuration probe created sessions: %v", err)
	}
}

func nativeHookCatalog(t *testing.T, ctx context.Context, command *exec.Cmd) ([]byte, map[string]hook) {
	t.Helper()
	client, err := appserver.Start(command)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := client.Shutdown(); err != nil {
			t.Errorf("join native configuration probe: %v", err)
		}
	}()
	id, err := client.Initialize()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reply(ctx, client, id); err != nil {
		t.Fatalf("initialize native probe: %v", err)
	}
	if _, err := client.Send("initialized", nil, false); err != nil {
		t.Fatal(err)
	}
	id, err = client.Send("hooks/list", map[string]any{"cwds": []string{command.Dir}}, true)
	if err != nil {
		t.Fatal(err)
	}
	data, err := reply(ctx, client, id)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := catalog(data, command.Dir)
	if err != nil {
		t.Fatalf("native hook catalog: %v\n%s", err, data)
	}
	indexed := make(map[string]hook, len(entries))
	for _, entry := range entries {
		if _, duplicate := indexed[entry.Key]; duplicate {
			t.Fatalf("duplicate native hook key %q", entry.Key)
		}
		indexed[entry.Key] = entry
	}
	return data, indexed
}
