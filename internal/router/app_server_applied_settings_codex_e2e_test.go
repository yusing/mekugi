//go:build journal_e2e

package router

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Each UI/router integration and Codex host runs in a separate process. Only
// workspace/thread-scoped files survive, with no live parent or in-memory owner.
func TestAppServerAppliedSettingsStartupResumeFreshProcessNativeCodex(t *testing.T) {
	testAppServerAppliedSettingsFreshProcessNativeCodex(t, false)
}

func TestAppServerAppliedSettingsCommandResumeFreshProcessNativeCodex(t *testing.T) {
	testAppServerAppliedSettingsFreshProcessNativeCodex(t, true)
}

func testAppServerAppliedSettingsFreshProcessNativeCodex(t *testing.T, commandResume bool) {
	t.Helper()
	for _, tier := range []string{"priority", "default"} {
		t.Run(tier, func(t *testing.T) {
			provider := &appResumeProvider{}
			server := httptest.NewServer(responsesHandler(t.Context(), time.Minute, provider, nil, nil))
			defer server.Close()
			environment := routerFaultCodexEnvironment(t)
			var codexHome string
			for _, variable := range environment {
				if value, ok := strings.CutPrefix(variable, "CODEX_HOME="); ok {
					codexHome = value
				}
			}
			stateHome, workspace := t.TempDir(), t.TempDir()
			environment = append(environment,
				"XDG_STATE_HOME="+stateHome,
				"MEKUGI_SETTINGS_PROCESS_PROVIDER="+server.URL,
				"MEKUGI_SETTINGS_PROCESS_WORKSPACE="+workspace,
				"MEKUGI_SETTINGS_PROCESS_TIER="+tier,
				"MEKUGI_SETTINGS_PROCESS_RESUME_COMMAND="+strconv.FormatBool(commandResume))
			run := func(thread string) {
				t.Helper()
				ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAppServerAppliedSettingsProcessHelper$", "-test.v")
				cmd.Env = append(environment, "MEKUGI_SETTINGS_PROCESS_ACTIVE=1", "MEKUGI_SETTINGS_PROCESS_THREAD="+thread)
				if output, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("fresh UI process: %v\n%s", err, output)
				}
			}
			run("")
			threads := provider.snapshot()
			if len(threads) != 1 || threads[0] == "" {
				t.Fatalf("seed process inference identities: %q", threads)
			}
			provider.mu.Lock()
			first := provider.settings[0]
			provider.mu.Unlock()
			expectedEffort := first.Reasoning.Effort
			if tier == "priority" {
				expectedEffort = "high"
			}
			expectedTier := tier
			if tier == "default" {
				expectedTier = ""
			}
			provider.assertSettings(t, 0, "gpt-6-sol", expectedEffort, expectedTier)
			// Change process defaults, not explicit invocation flags. Saved nullable
			// defaults must beat these just as saved priority/high settings do.
			conflictingEffort := "low"
			if first.Reasoning.Effort == conflictingEffort {
				conflictingEffort = "high"
			}
			config := fmt.Sprintf("model = %q\nmodel_reasoning_effort = %q\nservice_tier = %q\n", "gpt-6-astra", conflictingEffort, "flex")
			if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte(config), 0600); err != nil {
				t.Fatal(err)
			}
			run(threads[0])
			threads = provider.snapshot()
			if len(threads) != 2 || threads[1] != threads[0] {
				t.Fatalf("resume process replayed or switched the task: %q", threads)
			}
			provider.assertSettings(t, 1, first.Model, expectedEffort, expectedTier)
		})
	}
}

func TestAppServerAppliedSettingsProcessHelper(t *testing.T) {
	if os.Getenv("MEKUGI_SETTINGS_PROCESS_ACTIVE") != "1" {
		t.Skip("isolated subprocess fixture")
	}
	thread, tier := os.Getenv("MEKUGI_SETTINGS_PROCESS_THREAD"), os.Getenv("MEKUGI_SETTINGS_PROCESS_TIER")
	commandResume := os.Getenv("MEKUGI_SETTINGS_PROCESS_RESUME_COMMAND") == "true"
	startupThread := thread
	if commandResume {
		// Exercise the native command in a fresh UI, not only startup resume.
		startupThread = ""
	}
	terminal := startAppResumeTerminal(t, func(ctx context.Context) *exec.Cmd {
		cmd := exec.CommandContext(ctx, "codex", "app-server", "-c",
			`model_providers.preview={name="applied-settings",base_url=`+strconv.Quote(os.Getenv("MEKUGI_SETTINGS_PROCESS_PROVIDER")+"/v1")+`,wire_api="responses",requires_openai_auth=false}`,
			"-c", `model_provider="preview"`, "-c", "features.plugins=false", "-c", "include_collaboration_mode_instructions=false")
		if thread == "" {
			cmd.Args = append(cmd.Args, "-c", `model="gpt-6-astra"`)
		}
		cmd.Env, cmd.Dir = os.Environ(), os.Getenv("MEKUGI_SETTINGS_PROCESS_WORKSPACE")
		return cmd
	}, startupThread)
	terminal.await("Ready")
	if thread == "" {
		terminal.send("/model gpt-6-sol\r")
		terminal.awaitMatch("model applied", func(screen string) bool {
			return strings.Contains(screen, "gpt-6-sol") && strings.Contains(screen, "Settings saved for next turn")
		})
		if tier == "priority" {
			terminal.send("/reasoning high\r")
			terminal.await("gpt-6-sol (high)")
		}
		terminal.send("/tier priority\r")
		terminal.awaitMatch("priority applied", func(screen string) bool {
			return strings.Contains(screen, "priority") && strings.Contains(screen, "Settings saved for next turn")
		})
		if tier == "default" {
			terminal.send("/tier default\r")
			terminal.awaitMatch("explicit default applied before first turn", func(screen string) bool {
				return strings.Contains(screen, "gpt-6-sol") && !strings.Contains(screen, "priority") && !strings.Contains(screen, "Updating settings")
			})
		}
	} else {
		if commandResume {
			terminal.send("/resume " + thread + "\r")
		}
		// The fresh thread may already inherit workspace settings. Its model
		// alone cannot prove that the target session finished restoring.
		terminal.await("One inference with restored session defaults")
		terminal.await("gpt-6-sol")
	}
	terminal.send("One inference with restored session defaults\r")
	terminal.await("Recovered after a retry.")
	terminal.await("completed")
	terminal.quit()
}
