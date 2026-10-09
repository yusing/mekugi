//go:build unix

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
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/claude"
	"github.com/yusing/mekugi/internal/session"
)

func TestNativeRuntimeChildCompactClaudeNative(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_NATIVE_CLAUDE") != "1" {
		t.Skip("requires installed native Claude and built bridge")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	t.Setenv(routerTestWorkerEnvironment, "1")
	t.Setenv("MEKUGI_RUNTIME_DIR", t.TempDir())
	nativeGuidanceFixtureConfig(t)
	t.Setenv("ANTHROPIC_API_KEY", "native-child-compact-fixture")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	const rootPrompt = "CHILD_COMPACT_ROOT"
	const childPrompt = "CHILD_COMPACT_TARGET"
	const siblingPrompt = "CHILD_COMPACT_SIBLING"
	const summary = "NATIVE_CHILD_COMPACT_SUMMARY: assigned child work completed. No skill contents retained."
	service, binding, _ := observationHTTPFixture(t)
	presentation, err := service.PrepareCompanion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	paths := make(map[string]string)
	for _, name := range []string{"root", "child", "sibling"} {
		dir := filepath.Join(binding.Workspace, name)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		paths[name] = filepath.Join(dir, "SKILL.md")
		if err := os.WriteFile(paths[name], []byte("Native skill acceptance body."), 0600); err != nil {
			t.Fatal(err)
		}
	}
	agents := filepath.Join(presentation.Plugin, "agents")
	if err := os.Mkdir(agents, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agents, "compact.md"), []byte("---\nname: compact\ndescription: Native child compact acceptance.\ntools: Read\nmodel: haiku\n---\nRead only your assigned skill.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	counts := make(map[string]int)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			w.WriteHeader(401)
			return
		}
		var packet map[string]any
		if err := json.UnmarshalRead(http.MaxBytesReader(w, r.Body, 8<<20), &packet); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		text := nativeGuidanceRequestText(packet["messages"])
		tools, _ := packet["tools"].([]any)
		content := []any{map[string]any{"type": "text", "text": "CHILD_COMPACT_ACCEPTED"}}
		stop := "end_turn"
		tool := func(id, name string, input map[string]any) {
			content = []any{map[string]any{"type": "tool_use", "id": id, "name": name, "input": input}}
			stop = "tool_use"
		}
		read := func(name string) { tool("compact-"+name+"-read", "Read", map[string]any{"file_path": paths[name]}) }
		if len(tools) > 0 {
			switch {
			case strings.Contains(text, rootPrompt):
				counts["root"]++
				switch counts["root"] {
				case 1:
					read("root")
				case 2, 3:
					prompt := siblingPrompt
					if counts["root"] == 3 {
						prompt = childPrompt
					}
					tool(fmt.Sprint("compact-agent-", counts["root"]), "Agent", map[string]any{"description": "Isolated child compact", "subagent_type": "mekugi:compact", "prompt": prompt, "run_in_background": false})
				}
			case strings.Contains(text, siblingPrompt):
				counts["sibling"]++
				if counts["sibling"] == 1 {
					read("sibling")
				}
			case strings.Contains(text, childPrompt):
				counts["child"]++
				if counts["child"] == 1 {
					read("child")
				} else if counts["child"] == 2 {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 201000 tokens > 200000 maximum"}}`))
					return

				}
			case strings.Contains(text, summary):
				counts["after-summary"]++
			default:
				if strings.Contains(text, "<summary>") {
					content = []any{map[string]any{"type": "text", "text": "<summary>" + summary + "</summary>"}}
					counts["summary"]++
				} else {
					t.Errorf("native continuation omitted its summary: %.250s", text[max(0, len(text)-250):])
				}
			}
		}
		nativeGuidanceProviderReply(w, packet, content, stop)
	}))
	defer provider.Close()
	t.Setenv("ANTHROPIC_BASE_URL", provider.URL)
	executable, err := exec.LookPath("claude")
	if err != nil {
		t.Fatal(err)
	}
	bridge, _ := filepath.Abs("../claude/bridge/dist/bridge.js")
	endpoint := service.Endpoint()
	client, err := claude.Start(ctx, "node", bridge, claude.Config{Cwd: binding.Workspace, Executable: executable, Model: "haiku", Companion: &claude.ObservationEndpoint{Socket: endpoint.Socket, Token: endpoint.Token, Plugin: presentation.Plugin, FrontendDirectory: presentation.FrontendDirectory, JournalSchema: presentation.JournalSchema}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	u := newRuntimeUI(ctx, client, "Claude Code", binding.Workspace)
	u.attachRuntimeObservation(service)
	t.Cleanup(func() { u.shell.diff.close(); u.shell.diffScreen.Close() })
	results := make(map[string]int)
	var boundary session.Event
	childLoaded := false
	for {
		select {
		case <-ctx.Done():
			t.Fatalf("native child compact timed out: %s", u.notice)
		case event, open := <-client.Events():
			if !open || event.Kind == "error" || event.Kind == "done" && event.Failed {
				t.Fatalf("native child compact failed: %+v", event)
			}
			if err := u.runtimeEvent(event); err != nil {
				t.Fatal(err)
			}
			switch event.Kind {
			case "ready":
				if err := client.Send(ctx, rootPrompt); err != nil {
					t.Fatal(err)
				}
			case "prompt":
				if err := client.Respond(ctx, session.Decision{ID: event.Prompt.ID, Allow: true}); err != nil {
					t.Fatal(err)
				}
			case "tool_result":
				if event.Failed {
					t.Fatalf("native tool failed: %s", event.Text)
				}
				results[event.ID]++
				if event.ID == "compact-child-read" {
					for _, names := range u.agents.activeSkills() {
						childLoaded = childLoaded || slices.Contains(names, "child")
					}
				}
			case "context":
				if event.AgentID != "" {
					boundary = event
				}
			case "done":
				func() {
					mu.Lock()
					defer mu.Unlock()
					if !childLoaded || boundary.AgentID == "" || boundary.ID == "" || boundary.SessionID != u.thread {
						t.Fatalf("missing loaded child or authenticated boundary: loaded=%t boundary=%+v", childLoaded, boundary)
					}
					service.owner.mu.Lock()
					_, bound := service.owner.bindings[ObservationBinding{Runtime: "claude", Session: u.thread, Workspace: binding.Workspace, Agent: boundary.AgentID}]
					service.owner.mu.Unlock()
					if !bound || u.runtime.tasks[boundary.AgentID].ToolID != "compact-agent-3" {
						t.Fatal("compact receipt does not match native SubagentStart and parent tool identity")
					}
					for _, id := range []string{"compact-root-read", "compact-sibling-read", "compact-child-read", "compact-agent-2", "compact-agent-3"} {
						if results[id] != 1 {
							t.Fatalf("native tool %s ran %d times", id, results[id])
						}
					}
					if counts["summary"] != 1 || counts["after-summary"] != 1 {
						t.Fatalf("native summary was not retained in continuation: %v", counts)
					}
					assertNativeChildCompactSkills(t, u, boundary.AgentID)
				}()
				client.Close()
				resumed, err := claude.Start(ctx, "node", bridge, claude.Config{Cwd: binding.Workspace, Executable: executable, Model: "haiku", Resume: u.thread, Companion: &claude.ObservationEndpoint{Socket: endpoint.Socket, Token: endpoint.Token, Plugin: presentation.Plugin, FrontendDirectory: presentation.FrontendDirectory, JournalSchema: presentation.JournalSchema}})
				if err != nil {
					t.Fatal(err)
				}
				defer resumed.Close()
				fresh := newRuntimeUI(ctx, resumed, "Claude Code", binding.Workspace)
				fresh.attachRuntimeObservation(service)
				defer func() { fresh.shell.diff.close(); fresh.shell.diffScreen.Close() }()
				for event := range resumed.Events() {
					if event.Kind == "error" || event.Kind == "skill_history" && event.Failed {
						t.Fatalf("native resume failed: %+v", event)
					}
					if err := fresh.runtimeEvent(event); err != nil {
						t.Fatal(err)
					}
					if event.Kind == "ready" {
						assertNativeChildCompactSkills(t, fresh, boundary.AgentID)
						t.Log("installed native child compact clears only its loaded skills, preserves root/sibling and native summary, and restores correctly in a fresh bridge without tool replay")
						return
					}
				}
				t.Fatal("fresh bridge closed before native history restoration")
			}
		}
	}
}

func assertNativeChildCompactSkills(t *testing.T, u *appServerUI, child string) {
	t.Helper()
	for _, view := range []*liveActivityView{u.view, u.agents} {
		loads := view.activeSkills()
		if !slices.Equal(loads["Main"], []string{"root"}) || len(loads[runtimeTaskLane(child)]) != 0 {
			t.Fatalf("root/child current loads=%v", loads)
		}
		siblings := 0
		for owner, names := range loads {
			if owner != "Main" && owner != runtimeTaskLane(child) && slices.Equal(names, []string{"sibling"}) {
				siblings++
			}
		}
		if siblings != 1 {
			t.Fatalf("sibling context changed: %v", loads)
		}
	}
}
