//go:build unix

package router

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/claude"
	"github.com/yusing/mekugi/internal/shellsyntax"
)

// Installed native materializes settings and merges caller hooks. No prompt or
// inference is needed to prove that an unsafe startup never reaches ready.
func TestNativeRuntimeVCSGuardStartupClaudeNative(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_NATIVE_CLAUDE") != "1" {
		t.Skip("requires installed native Claude and built bridge; no inference")
	}
	executable, err := exec.LookPath("claude")
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := filepath.Abs("../claude/bridge/dist/bridge.js")
	if err != nil {
		t.Fatal(err)
	}
	helper, err := execTrackHelper()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, environment, value, event, matcher, rejection string
		background                                          bool
	}{
		{name: "project-bash-env", rejection: "native settings replaced the Bash startup observer"},
		{name: "legacy-bash-env", rejection: "native settings replaced the Bash startup observer"},
		{name: "missing-startup", rejection: "native startup resource unavailable"},
		{name: "missing-tracker", rejection: "native startup resource unavailable"},
		{name: "missing-helper", rejection: "Native startup environment was not confirmed"},
		{name: "shell-prefix", environment: "CLAUDE_CODE_SHELL_PREFIX", value: "env BASH_ENV=/dev/null", rejection: "native shell prefix can replace the startup observer"},
		{name: "unowned-session-environment", environment: "CLAUDE_ENV_FILE", value: "/dev/null", rejection: "Unowned native session environment"},
		{name: "caller-session-start", event: "SessionStart", rejection: "Competing native environment hook"},
		{name: "background-session-start", event: "SessionStart", background: true, rejection: "Competing native environment hook"},
		{name: "caller-bash-hook", event: "PreToolUse", matcher: "^Bash$", rejection: "Competing native Bash PreToolUse hook"},
		{name: "background-bash-hook", event: "PreToolUse", matcher: "^Bash$", background: true, rejection: "Competing native Bash PreToolUse hook"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			t.Setenv(routerTestWorkerEnvironment, "1")
			t.Setenv("MEKUGI_RUNTIME_DIR", t.TempDir())
			config := t.TempDir()
			t.Setenv("CLAUDE_CONFIG_DIR", config)
			legacyPath := filepath.Join(config, ".config.json")
			if tc.name == "legacy-bash-env" {
				// The existing legacy file takes precedence over .claude.json.
				// Native applies its global env separately from effective settings.
				nativeObservationWrite(t, legacyPath, `{"env":{"BASH_ENV":"/dev/null"}}`)
			}
			t.Setenv("ANTHROPIC_API_KEY", "native-startup-fixture")
			for _, key := range []string{"ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_SHELL_PREFIX", "CLAUDE_ENV_FILE"} {
				t.Setenv(key, "")
			}
			t.Setenv("CLAUDE_CODE_SHELL", "/bin/bash")
			if tc.environment != "" {
				t.Setenv(tc.environment, tc.value)
			}
			service, binding, _ := observationHTTPFixture(t)
			trace := traceNativeObservation(t, service)
			presentation, err := service.PrepareCompanion(ctx)
			if err != nil {
				t.Fatal(err)
			}
			previous := filepath.Join(t.TempDir(), "caller-startup")
			nativeObservationWrite(t, previous, "export NATIVE_STARTUP_FIXTURE=preserved\n")
			bashEnv, err := service.PrepareCommandTracking(ctx, helper, previous)
			if err != nil {
				t.Fatal(err)
			}
			if err := service.PrepareVCSGuard(ctx, helper); err != nil {
				t.Fatal(err)
			}
			if tc.name == "missing-startup" || tc.name == "missing-tracker" {
				resource := bashEnv
				if tc.name == "missing-tracker" {
					resource = filepath.Join(filepath.Dir(bashEnv), "exec-track.bash")
				}
				data, err := os.ReadFile(resource)
				if err != nil {
					t.Fatal(err)
				}
				// Restore only the fixture resource so normal owner cleanup can
				// validate the rest of its owned files without a missing-path error.
				t.Cleanup(func() { os.WriteFile(resource, data, 0600) })
				if err := os.Remove(resource); err != nil {
					t.Fatal(err)
				}
			}
			settings := map[string]any{}
			marker := filepath.Join(binding.Workspace, "caller-hook-marker")
			if tc.name == "project-bash-env" {
				settings["env"] = map[string]string{"BASH_ENV": "/dev/null"}
			}
			if tc.event != "" {
				// A competing carrier is deliberately harmless even if native runs
				// SessionStart before returning its effective hook listing.
				hook := map[string]any{"type": "command", "command": "printf 'caller-hook\\n' >> " + shellsyntax.Quote(marker)}
				if tc.background {
					hook["async"] = true
				}
				settings["hooks"] = map[string]any{tc.event: []any{map[string]any{"matcher": tc.matcher, "hooks": []any{hook}}}}
			}
			original, err := json.Marshal(&settings)
			if err != nil {
				t.Fatal(err)
			}
			settingsPath := filepath.Join(binding.Workspace, ".claude", "settings.json")
			if err := os.Mkdir(filepath.Dir(settingsPath), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(settingsPath, original, 0600); err != nil {
				t.Fatal(err)
			}
			var requests atomic.Int32
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/messages") {
					requests.Add(1)
					t.Error("native requested a model before startup admission")
				}
				w.WriteHeader(http.StatusUnauthorized)
			}))
			defer provider.Close()
			t.Setenv("ANTHROPIC_BASE_URL", provider.URL)
			endpoint := service.Endpoint()
			probeHelper := helper
			if tc.name == "missing-helper" {
				probeHelper = filepath.Join(t.TempDir(), "absent-helper")
			}
			client, err := claude.Start(ctx, "node", bridge, claude.Config{Cwd: binding.Workspace, Executable: executable, Model: "haiku", Companion: &claude.ObservationEndpoint{Socket: endpoint.Socket, Token: endpoint.Token, Plugin: presentation.Plugin, FrontendDirectory: presentation.FrontendDirectory, JournalSchema: presentation.JournalSchema, BashEnv: bashEnv, VCSGuard: true, VCSGuardHelper: probeHelper}})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			var rejection string
			for rejection == "" {
				select {
				case <-ctx.Done():
					t.Fatal("native startup admission timed out")
				case event, ok := <-client.Events():
					if !ok {
						t.Fatal("native closed without an explicit startup rejection")
					}
					switch event.Kind {
					case "ready":
						t.Fatal("unsafe native startup was admitted")
					case "error":
						rejection = event.Text
					}
				}
			}
			client.Close()
			if !strings.Contains(rejection, tc.rejection) {
				t.Fatalf("wrong startup rejection: %s", rejection)
			}
			if requests.Load() != 0 {
				t.Fatalf("model requests before admission: %d", requests.Load())
			}
			trace.mu.Lock()
			before, after := len(trace.before), len(trace.after)
			trace.mu.Unlock()
			if before != 0 || after != 0 {
				t.Fatalf("tool observation before admission: before=%d after=%d", before, after)
			}
			got, err := os.ReadFile(settingsPath)
			if err != nil || !bytes.Equal(got, original) {
				t.Fatalf("native startup changed caller settings: %v", err)
			}
			if tc.name == "legacy-bash-env" {
				// Native owns unrelated legacy session bookkeeping. The caller's
				// actual override must survive rejection without being repaired.
				got, err := os.ReadFile(legacyPath)
				if err != nil {
					t.Fatal(err)
				}
				var legacy struct {
					Env map[string]string `json:"env"`
				}
				if err := json.Unmarshal(got, &legacy); err != nil || legacy.Env["BASH_ENV"] != "/dev/null" {
					t.Fatalf("native startup changed the legacy override: %v", err)
				}
			}
			if tc.event == "PreToolUse" {
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					t.Fatalf("native Bash hook ran before admission: %v", err)
				}
			}
		})
	}
}
