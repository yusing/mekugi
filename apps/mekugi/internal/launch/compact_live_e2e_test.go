//go:build codex_e2e && e2e

package launch

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/executionobservation"
	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/modules/agentguidance"
	segmenttransport "github.com/yusing/mekugi/modules/commandsegments/transport"
	"github.com/yusing/mekugi/providers/openai"
)

// Live generation, selection and continuation all use the built-in OpenAI
// provider. Updated fixture contents can reach inference only through reloads.
func TestCompactReloadLiveOpenAI(t *testing.T) {
	model := strings.TrimSpace(os.Getenv("MEKUGI_E2E_MODEL"))
	if model == "" {
		t.Fatal("MEKUGI_E2E_MODEL must be set")
	}
	registry, err := buildToolRegistryForTest(t, t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	workspace := t.TempDir()
	if out, err := exec.Command("git", "init", "--quiet", workspace).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	source := filepath.Join(workspace, "guide.txt")
	skill := filepath.Join(workspace, "SKILL.md")
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(source, "Reusable project guidance. Current file token: FILE_BEFORE.\n")
	write(skill, "---\nname: reload-check\ndescription: reusable continuation guidance\n---\nCurrent skill token: SKILL_BEFORE.\n")
	proxy := newProxyWithSharedTestRegistry(t, registry)
	store, err := openMekugiReplayStore(registry.ReplayDirectory)
	if err != nil {
		t.Fatal(err)
	}
	setTestProxyStore(proxy, store)
	provider := &liveCompactProvider{Forwarder: newTestProviderClient(openai.BaseURL, http.DefaultClient)}
	executor := testRequestExecutor(provider, nil, proxy)
	executor.CompactPrompt = func() string {
		return "Create a concise handover of the conversation." + agentguidance.CompactInstructions
	}
	executor.CompactStore = executionobservation.New(executionobservation.Config{Retained: proxy.retained}).StoreCompact
	handler := inferencegateway.ResponsesHandler(t.Context(), time.Minute, executor.ExecuteHTTP)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			http.Error(w, "use HTTP", http.StatusUpgradeRequired)
			return
		}
		handler(w, r)
	}))
	defer server.Close()
	// Copy authentication privately into the isolated host configuration.
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
	environment := builtInOpenAICodexEnvironment(t)
	for _, entry := range environment {
		if path, ok := strings.CutPrefix(entry, "CODEX_HOME="); ok {
			if err := os.WriteFile(filepath.Join(path, "auth.json"), auth, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	environment, err = frontendShellEnvironment(environment, filepath.Dir(registry.Frontends["mcat"]), "", nil, segmenttransport.Endpoints{})
	if err != nil {
		t.Fatal(err)
	}
	environment = append(environment, routerTestWorkerEnvironment+"=1")
	terminal := startAppResumeTerminalWithIntegration(t, func(ctx context.Context) *exec.Cmd {
		args := codexArgs(server.URL+"/v1", []string{"app-server", "-c", fmt.Sprintf("model=%q", model), "-c", "features.plugins=false", "-c", "model_reasoning_effort=\"low\"", "-c", fmt.Sprintf("compact_prompt=%q", executor.CompactPrompt())}, true, false, true)
		cmd := exec.CommandContext(ctx, "codex", args...)
		cmd.Env, cmd.Dir = environment, workspace
		return cmd
	}, "", testAppServerIntegration(proxy))
	terminal.await("Ready")
	terminal.send("Read guide.txt and SKILL.md using exec. SKILL.md contains the active native skill named reload-check. Treat the file and skill as reusable instructions for future turns. This task is then complete; there is no pending implementation work. Reply exactly INITIAL_DONE.\r")
	terminal.await("INITIAL_DONE")
	terminal.await("╭─ Completed")
	terminal.send("/compact\r")
	terminal.await("Context compacted")
	write(source, "Reusable project guidance. Current file token: FILE_AFTER_7391.\n")
	write(skill, "---\nname: reload-check\ndescription: reusable continuation guidance\n---\nCurrent skill token: SKILL_AFTER_8264.\n")
	terminal.send("Without calling any tools yourself, report the current file and skill tokens from the reloaded context.\r")
	terminal.await("FILE_AFTER_7391")
	terminal.await("SKILL_AFTER_8264")
	terminal.await("Attached")
	terminal.await("Attached skill")
	terminal.await("(auto reloaded)")
	if !provider.reloaded.Load() {
		t.Fatal("fresh file and skill results did not reach live provider as reloaded context")
	}
	terminal.stopCanceled()
	t.Log("actual /compact: live built-in OpenAI summary selected idle guidance and skill; fresh native reads reached live continuation")
}

type liveCompactProvider struct {
	inferencegateway.Forwarder
	reloaded atomic.Bool
}

func (p *liveCompactProvider) ForwardExecution(ctx, execution context.Context, body []byte, headers http.Header, model string) (*inferencegateway.Response, error) {
	if bytes.Contains(body, []byte("FILE_AFTER_7391")) && bytes.Contains(body, []byte("SKILL_AFTER_8264")) && bytes.Contains(body, []byte("Reloaded context (tool output)")) {
		if i := bytes.Index(body, []byte("<mekugi-reload-v1>")); i >= 0 {
			return nil, fmt.Errorf("selection metadata leaked into continuation: %.300s", body[i:])
		}
		p.reloaded.Store(true)
	}
	return p.Forwarder.ForwardExecution(ctx, execution, body, headers, model)
}
