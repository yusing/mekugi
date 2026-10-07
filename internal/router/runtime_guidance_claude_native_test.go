package router

import (
	"context"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/claude"
)

// The native consumer sends to a scripted local provider. No model runs.
func TestRuntimeGuidanceClaudeNativeDelivery(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_NATIVE_CLAUDE") != "1" {
		t.Skip("requires installed native Claude and built bridge")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	t.Setenv(routerTestWorkerEnvironment, "1")
	t.Setenv("MEKUGI_RUNTIME_DIR", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("ANTHROPIC_API_KEY", "native-delivery-fixture")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	requests := make(chan map[string]any, 4)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/messages" {
			var packet map[string]any
			if err := json.UnmarshalRead(http.MaxBytesReader(w, r.Body, 8<<20), &packet); err != nil {
				t.Error(err)
			} else {
				select {
				case requests <- packet:
				default:
				}
				nativeGuidanceProviderReply(w, packet, []any{map[string]any{"type": "text", "text": "OK"}}, "end_turn")
				return
			}
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"local fixture: no model request accepted"}}`))
	}))
	defer provider.Close()
	t.Setenv("ANTHROPIC_BASE_URL", provider.URL)
	service, binding, _ := observationHTTPFixture(t)
	presentation, err := service.PrepareCompanion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := filepath.Abs("../claude/bridge/dist/bridge.js")
	if err != nil {
		t.Fatal(err)
	}
	executable, err := exec.LookPath("claude")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := service.Endpoint()
	capture := func(resume string) (map[string]any, string) {
		client, err := claude.Start(ctx, "node", bridge, claude.Config{Cwd: binding.Workspace, Executable: executable, Model: "haiku", Resume: resume, Companion: &claude.ObservationEndpoint{
			Socket: endpoint.Socket, Token: endpoint.Token, Plugin: presentation.Plugin, FrontendDirectory: presentation.FrontendDirectory, JournalSchema: presentation.JournalSchema,
		}})
		if err != nil {
			t.Fatal(err)
		}
		defer client.Close()
		var packet map[string]any
		var nativeSession string
		completed := false
		for packet == nil || nativeSession == "" || !completed {
			select {
			case <-ctx.Done():
				t.Fatal("native guidance request did not reach the local provider")
			case request := <-requests:
				system, _ := json.Marshal(request["system"])
				tools, _ := request["tools"].([]any)
				t.Logf("native request: model=%v max_tokens=%v system_bytes=%d tools=%d", request["model"], request["max_tokens"], len(system), len(tools))
				if len(tools) == 0 {
					continue
				} // Auxiliary native requests are not the work prompt.
				packet = request
			case event, ok := <-client.Events():
				if !ok {
					t.Fatal("native bridge ended before provider capture")
				}
				if event.Kind == "error" {
					t.Fatal(event.Text)
				}
				if event.Kind == "session" {
					nativeSession = event.SessionID
				}
				if event.Kind == "done" {
					if event.Failed {
						t.Fatalf("scripted native turn failed: %s", event.Text)
					}
					completed = true
				}
				if event.Kind == "ready" {
					if err := client.Send(ctx, "Implement a small integer-range function with tests."); err != nil {
						t.Fatal(err)
					}
				}
			}
		}
		return packet, nativeSession
	}
	packet, nativeSession := capture("")
	skillPath := filepath.Join(presentation.Plugin, "skills", "mekugi", "SKILL.md")
	skill, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatal(err)
	}
	_, body, ok := strings.Cut(string(skill), "\n---\n")
	if !ok {
		t.Fatal("generated skill has no body")
	}
	body = strings.TrimSpace(body)
	var system strings.Builder
	switch blocks := packet["system"].(type) {
	case string:
		system.WriteString(blocks)
	case []any:
		for _, value := range blocks {
			if block, ok := value.(map[string]any); ok {
				if text, ok := block["text"].(string); ok {
					system.WriteString(text)
				}
			}
		}
	}
	if !strings.Contains(system.String(), body) {
		t.Fatalf("complete production guidance is absent from native system context: native text=%d skill=%d tool name=%t", system.Len(), len(body), strings.Contains(system.String(), "mcp__mekugi__journal_batch"))
	}
	if !strings.Contains(system.String(), filepath.Join(filepath.Dir(skillPath), "frontends.md")) {
		t.Fatal("authenticated frontend contract path is absent")
	}
	assertNativeFrontendContracts(t, service.registry, system.String())
	names := make(map[string]bool)
	if tools, ok := packet["tools"].([]any); ok {
		for _, value := range tools {
			if tool, ok := value.(map[string]any); ok {
				if name, ok := tool["name"].(string); ok {
					names[name] = true
				}
			}
		}
	}
	for _, name := range []string{"mcp__mekugi__journal_batch", "mcp__mekugi__journal_read", "mcp__mekugi__mchanges"} {
		if !names[name] {
			t.Fatalf("native first prompt lacks %s", name)
		}
	}
	const current = "Current invocation guidance after native resume."
	if err := os.WriteFile(skillPath, append(skill, []byte("\n"+current+"\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	resumed, resumedSession := capture(nativeSession)
	if resumedSession != nativeSession {
		t.Fatal("native resume changed identity")
	}
	// Resume keeps its pinned system prompt. Current guidance must be in the
	// first input-hook context, without disabling that native snapshot.
	if !strings.Contains(nativeGuidanceRequestText(resumed["messages"]), body+"\n\n"+current) {
		t.Fatal("complete current guidance is absent from resumed input context")
	}
	assertNativeFrontendContracts(t, service.registry, resumed["messages"])
	t.Log("complete production guidance and MCP descriptors reached native fresh and resumed requests; no model ran")
}
