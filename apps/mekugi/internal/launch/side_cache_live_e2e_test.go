//go:build codex_e2e && e2e

package launch

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/providers/openai"
)

// A side fork's first inference must reuse its already-cached Main prefix.
func TestSideCacheEligibilityLiveOpenAI(t *testing.T) {
	model := strings.TrimSpace(os.Getenv("MEKUGI_E2E_MODEL"))
	if model == "" {
		t.Fatal("MEKUGI_E2E_MODEL must be set")
	}
	configRoot := os.Getenv("CODEX_HOME")
	if configRoot == "" {
		user, err := os.UserHomeDir()
		if err != nil {
			t.Fatal(err)
		}
		configRoot = filepath.Join(user, ".codex")
	}
	auth, err := os.ReadFile(filepath.Join(configRoot, "auth.json"))
	if err != nil {
		t.Fatal("live Codex authentication required")
	}
	for _, command := range []string{"/btw", "/side", "/oneoff"} {
		t.Run(strings.TrimPrefix(command, "/"), func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			upstream := http.DefaultTransport.(*http.Transport).Clone()
			defer upstream.CloseIdleConnections()
			transport := &cacheAffinityE2ETransport{delegate: upstream}
			proxy := newManagedMekugiProxy(t)
			handler := testResponsesHandler(t.Context(), 3*time.Minute,
				newTestProviderClient(openai.BaseURL, &http.Client{Transport: transport}), nil, proxy)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
					http.Error(w, "use HTTP", http.StatusUpgradeRequired)
					return
				}
				handler(w, r)
			}))
			defer server.Close()
			environment := builtInOpenAICodexEnvironment(t)
			for _, entry := range environment {
				if path, ok := strings.CutPrefix(entry, "CODEX_HOME="); ok {
					if err := os.WriteFile(filepath.Join(path, "auth.json"), auth, 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			workspace := t.TempDir()
			terminal := startAppResumeTerminalWithIntegration(t, func(ctx context.Context) *exec.Cmd {
				args := codexArgs(server.URL+"/v1", []string{"app-server", "-c", fmt.Sprintf("model=%q", model),
					"-c", "features.plugins=false", "-c", `model_reasoning_effort="low"`}, false, true)
				cmd := exec.CommandContext(ctx, "codex", args...)
				cmd.Env, cmd.Dir = environment, workspace
				return cmd
			}, "", testAppServerIntegration(proxy))
			terminal.await("Ready")
			terminal.send("Without using tools, join these parts with no spaces and reply with only the result: MAIN, _, READY.\r")
			terminal.await("MAIN_READY")
			terminal.await("╭─ Completed")
			terminal.send("Without using tools, join these parts with no spaces and reply with only the result: MAIN, _, WARM.\r")
			terminal.await("MAIN_WARM")
			terminal.await("╭─ Completed")
			seed, _, _ := transport.snapshot()
			if len(seed) < 2 || seed[len(seed)-1].InputTokens <= seed[len(seed)-1].UncachedInputTokens {
				t.Fatal("Main did not establish a cached prefix before the fork")
			}
			prompt := "Without using tools, join these parts with no spaces and reply with only the result: SIDE, _, READY."
			if command == "/side" {
				terminal.send("/side\r")
				terminal.awaitMatch("Side ready", func(frame string) bool {
					return strings.Contains(frame, "1 Side") && strings.Contains(frame, "Ready")
				})
				terminal.send(prompt + "\r")
			} else {
				terminal.send(command + " " + prompt + "\r")
			}
			terminal.await("SIDE_READY")
			if command == "/btw" {
				terminal.await("/btw · completed")
			} else {
				terminal.await("╭─ completed")
			}
			usages, _, _ := transport.snapshot()
			if len(usages) <= len(seed) {
				t.Fatalf("missing side usage: seed=%d final=%d", len(seed), len(usages))
			}
			for i, usage := range usages[len(seed):] {
				t.Logf("side request %d: input=%d cached=%d", i+1, usage.InputTokens, usage.InputTokens-usage.UncachedInputTokens)
			}
			first := usages[len(seed)]
			if first.InputTokens <= first.UncachedInputTokens {
				t.Error("first side request had no cached input tokens despite warmed Main")
			}
			terminal.stopCanceled()
		})
	}
}
