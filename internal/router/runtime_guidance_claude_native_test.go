package router

import (
	"context"
	json "encoding/json/v2"
	"fmt"
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
	testRuntimeGuidanceClaudeNativeDelivery(t, false, false)
}

func TestRuntimeGuidanceClaudeNativePromptMods(t *testing.T) {
	testRuntimeGuidanceClaudeNativeDelivery(t, true, false)
}

func TestRuntimeGuidanceClaudeNativeDisabled(t *testing.T) {
	testRuntimeGuidanceClaudeNativeDelivery(t, false, true)
}

func testRuntimeGuidanceClaudeNativeDelivery(t *testing.T, promptMods, disabled bool) {
	if os.Getenv("MEKUGI_TEST_NATIVE_CLAUDE") != "1" {
		t.Skip("requires installed native Claude and built bridge")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	t.Setenv(routerTestWorkerEnvironment, "1")
	t.Setenv("MEKUGI_RUNTIME_DIR", t.TempDir())
	nativeGuidanceFixtureConfig(t)
	settingsPath := filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), "settings.json")
	const disabledSettings = `{"disableAllHooks":true,"permissions":{"defaultMode":"default"}}`
	if disabled {
		if err := os.WriteFile(settingsPath, []byte(disabledSettings), 0600); err != nil {
			t.Fatal(err)
		}
	}
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
					t.Error("native guidance request capture capacity exceeded")
				}
				content := []any{map[string]any{"type": "text", "text": "OK"}}
				stop := "end_turn"
				tools, _ := packet["tools"].([]any)
				skillOffered := false
				for _, value := range tools {
					tool, _ := value.(map[string]any)
					skillOffered = skillOffered || tool["name"] == "Skill"
				}
				text := nativeGuidanceRequestText(packet)
				if len(tools) > 0 && !skillOffered && !strings.Contains(text, "native-disabled-skill") {
					content = []any{map[string]any{"type": "tool_use", "id": "native-disabled-skill", "name": "Skill", "input": map[string]any{"skill": "mekugi:mekugi"}}}
					stop = "tool_use"
				}
				nativeGuidanceProviderReply(w, packet, content, stop)
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
	if promptMods {
		installNativePromptModFixture(t, presentation.Plugin)
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
	invocation := 0
	capture := func(resume string, managed bool) (map[string]any, string) {
		invocation++
		client, err := claude.Start(ctx, "node", bridge, claude.Config{Cwd: binding.Workspace, Executable: executable, Model: "haiku", Resume: resume, Companion: &claude.ObservationEndpoint{
			Socket: endpoint.Socket, Token: endpoint.Token, Plugin: presentation.Plugin, FrontendDirectory: presentation.FrontendDirectory, ManagedSkills: managed, JournalSchema: presentation.JournalSchema,
		}})
		if err != nil {
			t.Fatal(err)
		}
		defer client.Close()
		if disabled {
			// Input queued before ready still waits for native guidance admission.
			if err := client.Send(ctx, "NATIVE_DISABLED_GUIDANCE_MUST_NOT_RUN"); err != nil {
				t.Fatal(err)
			}
			for {
				select {
				case <-ctx.Done():
					t.Fatal("disabled mandatory mod did not report unavailable guidance")
				case <-requests:
					t.Fatal("disabled guidance admitted a provider request")
				case event, ok := <-client.Events():
					if !ok {
						t.Fatal("native bridge ended without the guidance failure")
					}
					if event.Kind != "error" {
						continue
					}
					if !strings.Contains(event.Text, "Mandatory companion guidance unavailable") {
						t.Fatalf("unexpected native failure: %s", event.Text)
					}
					client.Close()
					if len(requests) != 0 {
						t.Fatal("disabled guidance admitted a provider request")
					}
					retained, err := os.ReadFile(settingsPath)
					if err != nil || string(retained) != disabledSettings {
						t.Fatal("guidance admission changed native settings")
					}
					return nil, ""
				}
			}
		}
		var nativeSession string
		blockedSkill := false
		turn := func(prompt string, waitReady bool) map[string]any {
			if !waitReady {
				if err := client.Send(ctx, prompt); err != nil {
					t.Fatal(err)
				}
			}
			var packet map[string]any
			completed := false
			for packet == nil || nativeSession == "" || !completed {
				select {
				case <-ctx.Done():
					t.Fatal("native guidance request did not reach the local provider")
				case request := <-requests:
					tools, _ := request["tools"].([]any)
					if packet == nil && len(tools) > 0 && strings.Contains(nativeGuidanceRequestText(request["messages"]), prompt) {
						packet = request // The first consuming request, before tool follow-ups.
					}
				case event, ok := <-client.Events():
					if !ok {
						t.Fatal("native bridge ended before provider capture")
					}
					switch event.Kind {
					case "error":
						t.Fatal(event.Text)
					case "session":
						nativeSession = event.SessionID
					case "tool_result":
						if event.ID == "native-disabled-skill" {
							if !event.Failed {
								t.Fatal("a disallowed native Skill call executed successfully")
							}
							blockedSkill = true
						}
					case "done":
						if event.Failed {
							t.Fatalf("scripted native turn failed: %s", event.Text)
						}
						completed = true
					case "ready":
						if err := client.Send(ctx, prompt); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			assertNativeStaticGuidance(t, packet, presentation.Plugin, service.registry)
			assertNativeCompanionTools(t, packet)
			return packet
		}
		packet := turn(fmt.Sprintf("Implement a small integer-range function with tests. GUIDANCE_INVOCATION_%d_INITIAL!", invocation), true)
		if managed && resume == "" {
			turn(fmt.Sprintf("Continue the same task without tools. GUIDANCE_INVOCATION_%d_REPEAT!", invocation), false)
		}
		if managed && resume == "" && !blockedSkill {
			t.Fatal("native disallowed Skill execution was not checked")
		}
		return packet, nativeSession
	}
	if disabled {
		capture("", presentation.ManagedSkills)
		return
	}
	if !presentation.ManagedSkills {
		t.Fatal("companion did not select the fixture's skills-mgr owner")
	}
	packet, nativeSession := capture("", presentation.ManagedSkills)
	if promptMods {
		assertNativePromptModFixture(t, packet, true)
	}
	assertNativeManagedSkillFixture(t, packet, presentation.ManagedSkills)
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
	if !strings.Contains(nativeGuidanceRequestText(packet["messages"]), filepath.Join(filepath.Dir(skillPath), "frontends.md")) {
		t.Fatal("authenticated frontend contract path is absent")
	}
	const current = "Current invocation guidance after native resume."
	if err := os.WriteFile(skillPath, append(skill, []byte("\n"+current+"\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	resumed, resumedSession := capture(nativeSession, presentation.ManagedSkills)
	if promptMods {
		assertNativePromptModFixture(t, resumed, true)
	}
	assertNativeManagedSkillFixture(t, resumed, presentation.ManagedSkills)
	if resumedSession != nativeSession {
		t.Fatal("native resume changed identity")
	}
	// Resume keeps its pinned system prompt. Current guidance must reach its
	// first work request without disabling that native snapshot.
	if !strings.Contains(nativeGuidanceRequestText(resumed["messages"]), body+"\n\n"+current) {
		t.Fatal("complete current guidance is absent from resumed input context")
	}
	if strings.Count(nativeGuidanceRequestText(resumed["messages"]), body) != 1 {
		t.Fatal("resumed mod retained or duplicated superseded workflow")
	}
	assertNativeFrontendContracts(t, service.registry, resumed["messages"])
	unmanaged, _ := capture("", false)
	assertNativeManagedSkillFixture(t, unmanaged, false)
	t.Log("complete current guidance and MCP tool availability reached fresh and resumed requests; no model ran")
}
