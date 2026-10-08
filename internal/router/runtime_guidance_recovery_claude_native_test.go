package router

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/claude"
	"github.com/yusing/mekugi/internal/session"
)

// Real native child and classic /compact consumers, with no model inference.
func TestRuntimeGuidanceClaudeNativeChildAndCompact(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_NATIVE_CLAUDE") != "1" {
		t.Skip("requires installed native Claude and built bridge")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	t.Setenv(routerTestWorkerEnvironment, "1")
	t.Setenv("MEKUGI_RUNTIME_DIR", t.TempDir())
	nativeGuidanceFixtureConfig(t)
	t.Setenv("ANTHROPIC_API_KEY", "native-recovery-fixture")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	const parentPrompt = "NATIVE_PARENT_GUIDANCE_FIXTURE"
	const childPrompt = "NATIVE_CHILD_GUIDANCE_FIXTURE"
	const afterPrompt = "NATIVE_AFTER_COMPACT_GUIDANCE_FIXTURE"
	const summary = "NATIVE_SUMMARY_RETAIN_CEDAR: the native child completed its one assigned task."
	var mu sync.Mutex
	var childPacket, compactPacket map[string]any
	var requests, parentRequests, childRequests, summaryRequests int
	var requestPhase int
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var packet map[string]any
		if err := json.UnmarshalRead(http.MaxBytesReader(w, r.Body, 8<<20), &packet); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		requests++
		if requests > 16 {
			t.Error("native fixture exceeded its bounded request budget")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		messages := nativeGuidanceRequestText(packet["messages"])
		tools, _ := packet["tools"].([]any)
		content := []any{map[string]any{"type": "text", "text": "OK"}}
		stop := "end_turn"
		switch {
		case len(tools) == 0:
			// Native title generation is not a work or summary request.
		case requestPhase == 1:
			summaryRequests++
			content = []any{map[string]any{"type": "text", "text": summary}}
		case requestPhase == 2 && strings.Contains(messages, afterPrompt):
			compactPacket = packet
		case strings.Contains(messages, parentPrompt):
			parentRequests++
			if parentRequests == 1 {
				content = []any{map[string]any{"type": "tool_use", "id": "native-child-once", "name": "Agent", "input": map[string]any{"description": "Native guidance consumer", "subagent_type": "mekugi:guidance-acceptance", "prompt": childPrompt, "run_in_background": false}}}
				stop = "tool_use"
			}
		case strings.Contains(messages, childPrompt):
			childRequests++
			childPacket = packet
			content = []any{map[string]any{"type": "text", "text": "CHILD_ACCEPTED"}}
		default:
			t.Errorf("unrecognized native work request: tools=%d", len(tools))
		}
		nativeGuidanceProviderReply(w, packet, content, stop)
	}))
	defer provider.Close()
	t.Setenv("ANTHROPIC_BASE_URL", provider.URL)
	service, binding, _ := observationHTTPFixture(t)
	presentation, err := service.PrepareCompanion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	agents := filepath.Join(presentation.Plugin, "agents")
	installNativePromptModFixture(t, presentation.Plugin)
	if err := os.Mkdir(agents, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agents, "guidance-acceptance.md"), []byte("---\nname: guidance-acceptance\ndescription: Isolated native guidance acceptance.\ntools: Read\nmodel: haiku\n---\nReturn CHILD_ACCEPTED without tools.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	skill, err := os.ReadFile(filepath.Join(presentation.Plugin, "skills", "mekugi", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	_, body, ok := strings.Cut(string(skill), "\n---\n")
	if !ok {
		t.Fatal("generated workflow has no body")
	}
	body = strings.TrimSpace(body)
	bridge, err := filepath.Abs("../claude/bridge/dist/bridge.js")
	if err != nil {
		t.Fatal(err)
	}
	executable, err := exec.LookPath("claude")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := service.Endpoint()
	client, err := claude.Start(ctx, "node", bridge, claude.Config{Cwd: binding.Workspace, Executable: executable, Model: "haiku", Companion: &claude.ObservationEndpoint{Socket: endpoint.Socket, Token: endpoint.Token, Plugin: presentation.Plugin, FrontendDirectory: presentation.FrontendDirectory, JournalSchema: presentation.JournalSchema}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	phase, agentResults, compactBoundaries := 0, 0, 0
	agentCalls := make(map[string]bool)
	var nativeSession string
	for phase < 3 {
		select {
		case <-ctx.Done():
			t.Fatalf("native recovery timed out in phase %d", phase)
		case event, ok := <-client.Events():
			if !ok {
				t.Fatal("native bridge ended before acceptance")
			}
			switch event.Kind {
			case "ready":
				if err := client.Send(ctx, parentPrompt); err != nil {
					t.Fatal(err)
				}
			case "session":
				if nativeSession != "" && event.SessionID != nativeSession {
					t.Fatal("classic compaction changed native session identity")
				}
				nativeSession = event.SessionID
			case "prompt":
				if event.Prompt == nil {
					t.Fatal("missing native permission prompt")
				}
				if err := client.Respond(ctx, session.Decision{ID: event.Prompt.ID, Allow: true}); err != nil {
					t.Fatal(err)
				}
			case "tool":
				if event.Role == "Agent" {
					agentCalls[event.ID] = true // Streaming and complete views share one ID.
				}
			case "tool_result":
				if event.Failed {
					t.Fatalf("native tool failed: %s", event.Text)
				}
				if event.ID == "native-child-once" {
					agentResults++
				}
			case "notice":
				if event.Text == "Native context compaction completed" {
					compactBoundaries++
				}
			case "error":
				t.Fatal(event.Text)
			case "done":
				if event.Failed {
					t.Fatal(event.Text)
				}
				phase++
				if phase < 3 {
					mu.Lock()
					requestPhase = phase
					mu.Unlock()
					prompt := "/compact"
					if phase == 2 {
						prompt = afterPrompt
					}
					if err := client.Send(ctx, prompt); err != nil {
						t.Fatal(err)
					}
				}
			}
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if childPacket == nil || compactPacket == nil {
		t.Fatalf("native consumer requests missing: child=%t compact=%t requests=%d summaries=%d", childPacket != nil, compactPacket != nil, requests, summaryRequests)
	}
	for name, packet := range map[string]map[string]any{"child": childPacket, "post-compact": compactPacket} {
		// A native custom child's prompt has no preset session_guidance section.
		assertNativePromptModFixture(t, packet, name != "child")
		if name == "child" && !strings.Contains(nativeGuidanceRequestText(packet["system"]), "Return CHILD_ACCEPTED without tools.") {
			t.Fatal("prompt mods replaced the native custom child's system instructions")
		}
		// Inspect messages, not the inherited root system prompt. This proves
		// native hook context reached the specific consuming model request.
		if !strings.Contains(nativeGuidanceRequestText(packet["messages"]), body) {
			t.Fatalf("complete production workflow absent from native %s messages", name)
		}
		assertNativeFrontendContracts(t, service.registry, packet["messages"])
	}
	if !strings.Contains(nativeGuidanceRequestText(compactPacket["messages"]), summary) {
		t.Fatalf("classic recovery replaced or omitted the native summary: summary requests=%d parent requests=%d", summaryRequests, parentRequests)
	}
	if len(agentCalls) != 1 || !agentCalls["native-child-once"] || agentResults != 1 || childRequests != 1 || parentRequests != 2 || compactBoundaries != 1 || summaryRequests != 1 {
		t.Fatalf("native execution duplicated or incomplete: Agent=%d results=%d child=%d parent=%d compact=%d summaries=%d", len(agentCalls), agentResults, childRequests, parentRequests, compactBoundaries, summaryRequests)
	}
	service.owner.mu.Lock()
	defer service.owner.mu.Unlock()
	children := 0
	for b := range service.owner.bindings {
		if b.Agent != "" {
			children++
			if b.Session != nativeSession || b.Workspace != binding.Workspace {
				t.Fatal("native child binding borrowed another session or workspace")
			}
		}
	}
	if children != 1 || nativeSession == "" {
		t.Fatalf("native SubagentStart identity missing or duplicated: children=%d", children)
	}
	t.Logf("native child and classic compact consumed full workflow; native summary preserved; %d local requests, no inference", requests)
}

func nativeGuidanceRequestText(value any) string {
	var texts []string
	var collect func(any)
	collect = func(value any) {
		switch value := value.(type) {
		case string:
			texts = append(texts, value)
		case []any:
			for _, child := range value {
				collect(child)
			}
		case map[string]any:
			for _, child := range value {
				collect(child)
			}
		}
	}
	collect(value)
	return strings.Join(texts, "\n")
}

func assertNativeFrontendContracts(t *testing.T, registry *toolRegistry, carrier any) {
	t.Helper()
	text := nativeGuidanceRequestText(carrier)
	count := 0
	for _, entry := range registry.ordered {
		if !entry.Executable || len(entry.Specification) == 0 {
			continue
		}
		var spec struct {
			Description string `json:"description"`
		}
		if err := json.Unmarshal(entry.Specification, &spec); err != nil {
			t.Fatal(err)
		}
		if spec.Description == "" || !strings.Contains(text, spec.Description) {
			t.Fatalf("complete authenticated %s contract is absent from native context", entry.Name)
		}
		count++
	}
	if count == 0 {
		t.Fatal("native guidance check selected no authenticated frontends")
	}
}

func nativeGuidanceProviderReply(w http.ResponseWriter, packet map[string]any, content []any, stop string) {
	message := map[string]any{"id": "fixture-native-recovery", "type": "message", "role": "assistant", "model": "claude-haiku-4-5-20251001", "content": content, "stop_reason": stop, "usage": map[string]any{"input_tokens": 1, "output_tokens": 1}}
	if stream, _ := packet["stream"].(bool); !stream {
		w.Header().Set("Content-Type", "application/json")
		_ = json.MarshalWrite(w, message)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	emit := func(event string, value any) {
		encoded, _ := json.Marshal(value)
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, encoded)
	}
	message["content"] = []any{}
	message["stop_reason"] = nil
	emit("message_start", map[string]any{"type": "message_start", "message": message})
	for index, block := range content {
		value := block.(map[string]any)
		start := map[string]any{"type": value["type"]}
		var delta map[string]any
		if value["type"] == "tool_use" {
			start["id"], start["name"], start["input"] = value["id"], value["name"], map[string]any{}
			input, _ := json.Marshal(value["input"])
			delta = map[string]any{"type": "input_json_delta", "partial_json": string(input)}
		} else {
			start["text"] = ""
			delta = map[string]any{"type": "text_delta", "text": value["text"]}
		}
		emit("content_block_start", map[string]any{"type": "content_block_start", "index": index, "content_block": start})
		emit("content_block_delta", map[string]any{"type": "content_block_delta", "index": index, "delta": delta})
		emit("content_block_stop", map[string]any{"type": "content_block_stop", "index": index})
	}
	emit("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": stop, "stop_sequence": nil}, "usage": map[string]any{"output_tokens": 1}})
	_, _ = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
}
