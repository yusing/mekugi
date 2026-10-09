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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/claude"
)

// The installed SDK produces both current-request and cumulative usage. Shared
// UI dispatch must select only the former, retaining native source history.
func TestNativeRuntimeSubsliceClaudeNative(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_NATIVE_CLAUDE") != "1" {
		t.Skip("requires installed native Claude and built bridge")
	}
	for _, tc := range []struct {
		name        string
		input       *uint64
		root, reset bool
	}{
		{name: "root", input: new(uint64(1)), root: true, reset: true},
		{name: "low", input: new(uint64(1))},
		{name: "high", input: new(uint64(150000)), reset: true},
		{name: "unknown", reset: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 75*time.Second)
			defer cancel()
			t.Setenv(routerTestWorkerEnvironment, "1")
			t.Setenv("MEKUGI_RUNTIME_DIR", t.TempDir())
			nativeGuidanceFixtureConfig(t)
			t.Setenv("ANTHROPIC_API_KEY", "native-subslice-fixture")
			t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
			t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
			const prompt = "SUBSLICE_PRIVATE_SOURCE"
			var mu sync.Mutex
			requests := 0
			continued := false
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
				requests++
				if requests > 12 {
					t.Error("native subslice exceeded request budget")
					w.WriteHeader(400)
					return
				}
				text := nativeGuidanceRequestText(packet["messages"])
				content := []any{map[string]any{"type": "text", "text": "SUBSLICE_ACCEPTED"}}
				stop := "end_turn"
				input := tc.input
				tools, _ := packet["tools"].([]any)
				if len(tools) > 0 {
					switch {
					case strings.Contains(text, "Continue the journal"):
						continued = true
						if strings.Contains(text, prompt) == tc.reset {
							t.Errorf("reset=%v retained source conversation=%v", tc.reset, strings.Contains(text, prompt))
						}
					case !strings.Contains(text, "subslice-effect"):
						content = []any{map[string]any{"type": "tool_use", "id": "subslice-effect", "name": "Bash", "input": map[string]any{"command": "printf 'once\\n' >> effects.txt", "description": "Once-only source effect"}}}
						stop, input = "tool_use", new(uint64(80000))
					case !strings.Contains(text, "subslice-plan"):
						ops := []any{map[string]any{"op": "add", "kind": "task", "title": "Parent", "state": "working"}, map[string]any{"op": "plan", "under": "/1", "reset": "slice", "tasks": []any{map[string]any{"title": "First", "state": "working"}, map[string]any{"title": "Next"}}}, map[string]any{"op": "set", "p": "/1/1", "state": "done"}}
						if tc.root {
							ops = []any{map[string]any{"op": "plan", "reset": "slice", "tasks": []any{map[string]any{"title": "First", "state": "working"}, map[string]any{"title": "Next"}}}, map[string]any{"op": "set", "p": "/1", "state": "done"}}
						}
						content = []any{map[string]any{"type": "tool_use", "id": "subslice-plan", "name": "mcp__mekugi__journal_batch", "input": map[string]any{"journal": ops}}}
						stop, input = "tool_use", new(uint64(80000))
					}
				}
				// Reuse the established wire fixture, replacing only provider usage.
				reply := httptest.NewRecorder()
				nativeGuidanceProviderReply(reply, packet, content, stop)
				body := reply.Body.String()
				lines := strings.Split(body, "\n")
				for i, line := range lines {
					if !strings.HasPrefix(line, "data: ") {
						continue
					}
					var event map[string]any
					if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
						t.Error(err)
						continue
					}
					if message, ok := event["message"].(map[string]any); ok {
						message["id"] = fmt.Sprintf("subslice-message-%d", requests)
						usage := message["usage"].(map[string]any)
						if input == nil {
							delete(usage, "input_tokens")
						} else {
							usage["input_tokens"] = *input
						}
					}
					encoded, _ := json.Marshal(event)
					lines[i] = "data: " + string(encoded)
				}
				body = strings.Join(lines, "\n")
				w.Header().Set("Content-Type", reply.Header().Get("Content-Type"))
				_, _ = fmt.Fprint(w, body)
			}))
			defer provider.Close()
			t.Setenv("ANTHROPIC_BASE_URL", provider.URL)
			executable, err := exec.LookPath("claude")
			if err != nil {
				t.Fatal(err)
			}
			bridge, err := filepath.Abs("../claude/bridge/dist/bridge.js")
			if err != nil {
				t.Fatal(err)
			}
			binding := ObservationBinding{Runtime: "claude", Workspace: t.TempDir()}
			storage := t.TempDir()
			launch := func(resume string) (*claude.Client, *appServerUI, func()) {
				service, _, closeService := observationIsolationService(t, storage, binding)
				p, err := service.PrepareCompanion(ctx)
				if err != nil {
					t.Fatal(err)
				}
				ep := service.Endpoint()
				client, err := claude.Start(ctx, "node", bridge, claude.Config{Cwd: binding.Workspace, Executable: executable, Model: "haiku", Resume: resume, Companion: &claude.ObservationEndpoint{Socket: ep.Socket, Token: ep.Token, Plugin: p.Plugin, FrontendDirectory: p.FrontendDirectory, ManagedSkills: p.ManagedSkills, JournalSchema: p.JournalSchema}})
				if err != nil {
					t.Fatal(err)
				}
				u := newRuntimeUI(ctx, client, "Claude Code", binding.Workspace)
				u.attachRuntimeObservation(service)
				close := sync.OnceFunc(func() {
					client.Close()
					u.finishCommandSegments()
					u.shell.diff.close()
					u.shell.diffScreen.Close()
					closeService()
				})
				t.Cleanup(close)
				return client, u, close
			}
			resets := 0
			consume := func(client *claude.Client, u *appServerUI, until string) {
				done := false
				for {
					select {
					case <-ctx.Done():
						t.Fatalf("native subslice timed out: %s", u.notice)
					case e, open := <-client.Events():
						if !open {
							t.Fatal("native bridge closed")
						}
						if e.Kind == "error" || e.Kind == "done" && e.Failed || e.Kind == "tool_result" && e.Failed {
							t.Fatalf("native failure: %+v", e)
						}
						if err := u.runtimeEvent(e); err != nil {
							t.Fatal(err)
						}
						if e.Kind == "reset" {
							resets++
						}
						if e.Kind == "prompt" {
							if e.Prompt == nil {
								t.Fatal("missing permission")
							}
							runtimeFrame(t, u, 120, 32)
							runtimeKeys(t, u, "1\r")
						}
						if e.Kind == "done" {
							done = true
						}
						if until == "done" && done && e.Kind == "usage" || until != "done" && e.Kind == until {
							return
						}
					}
				}
			}
			client, u, close := launch("")
			consume(client, u, "ready")
			if err := u.beginRuntimeJournalTurn(); err != nil {
				t.Fatal(err)
			}
			if err := client.Send(ctx, prompt); err != nil {
				t.Fatal(err)
			}
			consume(client, u, "done")
			source := u.thread
			if source == "" || u.runtime.continuation == nil {
				t.Fatal("missing source identity or continuation")
			}
			if tc.input == nil {
				if u.runtime.contextTokens != nil {
					t.Fatalf("unknown context became %d", *u.runtime.contextTokens)
				}
			} else if u.runtime.contextTokens == nil || *u.runtime.contextTokens != *tc.input+1 {
				t.Fatalf("current request context=%v want=%d", u.runtime.contextTokens, *tc.input+1)
			}
			var cumulative uint64
			if u.runtime.usage != nil {
				for _, usage := range u.runtime.usage.Models {
					if usage.Input != nil {
						cumulative += *usage.Input
					}
				}
			}
			if cumulative < 150000 {
				t.Fatalf("fixture did not distinguish cumulative usage: %d", cumulative)
			}
			if err := u.tickRuntimeJournal(u.now()); err != nil {
				t.Fatal(err)
			}
			if err := u.tickRuntimeJournal(u.now().Add(4 * time.Second)); err != nil {
				t.Fatal(err)
			}
			if u.runtime.continuation == nil && !u.runtime.busy && u.runtime.resetRequest == "" {
				t.Fatalf("continuation did not dispatch: draft=%q questions=%d notice=%s", u.draft, u.questionCount(), u.notice)
			}
			consume(client, u, "done")
			if (u.thread != source) != tc.reset || (resets == 1) != tc.reset {
				t.Fatalf("reset=%v source=%s current=%s receipts=%d", tc.reset, source, u.thread, resets)
			}
			close()
			mu.Lock()
			before := requests
			wasContinued := continued
			mu.Unlock()
			if !wasContinued {
				t.Fatal("native provider did not consume the continuation")
			}
			client, restored, close := launch(source)
			consume(client, restored, "ready")
			if restored.thread != source {
				t.Fatal("fresh resume lost source identity")
			}
			for _, id := range []string{"subslice-effect", "subslice-plan"} {
				found := false
				for _, row := range restored.view.entries {
					if row.CallID == "history/"+id {
						found = true
					}
				}
				if !found {
					t.Fatalf("source history lost %s", id)
				}
			}
			if tc.name == "low" && (restored.runtime.contextTokens == nil || *restored.runtime.contextTokens >= 150000) {
				t.Fatal("fresh native history did not restore known-low context before input")
			}
			if tc.name == "high" && (restored.runtime.contextTokens == nil || *restored.runtime.contextTokens < 150000) {
				t.Fatal("fresh native history lost high context before input")
			}
			close()
			mu.Lock()
			after := requests
			mu.Unlock()
			if after != before {
				t.Fatalf("fresh resume replayed model requests: %d/%d", after, before)
			}
			effects, err := os.ReadFile(filepath.Join(binding.Workspace, "effects.txt"))
			if err != nil || string(effects) != "once\n" {
				t.Fatalf("native effects=%q err=%v", effects, err)
			}
		})
	}
}
