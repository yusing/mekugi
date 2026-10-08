//go:build unix

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
	"sync"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/claude"
	"github.com/yusing/mekugi/internal/execsegment"
	"github.com/yusing/mekugi/internal/session"
)

// Native transcripts, including official child history, authorize retained
// segments after every original query and observation owner has been closed.
func TestNativeRuntimeSavedSegmentsClaudeNative(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_NATIVE_CLAUDE") != "1" {
		t.Skip("requires installed native Claude and built bridge")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Second)
	defer cancel()
	t.Setenv(routerTestWorkerEnvironment, "1")
	t.Setenv("MEKUGI_RUNTIME_DIR", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("ANTHROPIC_API_KEY", "native-saved-segments-fixture")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	t.Setenv("CLAUDE_CODE_SHELL", "/bin/bash")
	const rootPrompt = "SAVED_SEGMENTS_ROOT"
	const childPrompt = "SAVED_SEGMENTS_CHILD"
	const mutationCommand = "printf 'POST_FORK_CHILD_OUTPUT\\n'; printf 'post-fork\\n' >> effects.txt"
	commands := map[string]string{
		"saved-root-bash":  "printf 'ROOT_OUTPUT\\n'; printf 'root\\n' >> effects.txt; false && printf 'SKIPPED\\n'",
		"saved-child-bash": "printf 'CHILD_OUTPUT\\n'; printf 'child\\n' >> effects.txt; printf 'CHILD_FINAL\\n'",
	}
	var mu sync.Mutex
	requests, rootRequests, childRequests := 0, 0, 0
	var nativeChild string
	mutation := false
	mutationRoot, mutationChild := 0, 0
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
		if requests > 24 {
			t.Error("native saved segments exceeded request budget")
			w.WriteHeader(400)
			return
		}
		content := []any{map[string]any{"type": "text", "text": "SAVED_ACCEPTED"}}
		stop := "end_turn"
		tools, _ := packet["tools"].([]any)
		if len(tools) > 0 {
			text := nativeGuidanceRequestText(packet["messages"])
			var id string
			switch {
			case mutation && strings.Contains(text, "SAVED_PARENT_POST_FORK"):
				mutationRoot++
				if mutationRoot == 1 {
					content = []any{map[string]any{"type": "tool_use", "id": "saved-child-followup-agent", "name": "Agent", "input": map[string]any{"description": "Native child follow-up after fork", "subagent_type": "mekugi:saved-segments", "resume": nativeChild, "prompt": "SAVED_CHILD_POST_FORK", "run_in_background": false}}}
					stop = "tool_use"
				}
			case mutation && strings.Contains(text, "SAVED_CHILD_POST_FORK"):
				mutationChild++
				if mutationChild == 1 {
					content = []any{map[string]any{"type": "tool_use", "id": "saved-child-after-fork", "name": "Bash", "input": map[string]any{"command": mutationCommand, "description": "Native child history isolation"}}}
					stop = "tool_use"
				}
			case strings.Contains(text, rootPrompt):
				rootRequests++
				if rootRequests == 1 {
					id = "saved-root-bash"
				}
				if rootRequests == 2 {
					content = []any{map[string]any{"type": "tool_use", "id": "saved-child-agent", "name": "Agent", "input": map[string]any{"description": "Saved native segments", "subagent_type": "mekugi:saved-segments", "prompt": childPrompt}}}
					stop = "tool_use"
				}
			case strings.Contains(text, childPrompt):
				childRequests++
				if childRequests == 1 {
					id = "saved-child-bash"
				}
			}
			if id != "" {
				content = []any{map[string]any{"type": "tool_use", "id": id, "name": "Bash", "input": map[string]any{"command": commands[id], "description": "Retain exact native segments"}}}
				stop = "tool_use"
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
	bridge, err := filepath.Abs("../claude/bridge/dist/bridge.js")
	if err != nil {
		t.Fatal(err)
	}
	binding := ObservationBinding{Runtime: "claude", Workspace: t.TempDir()}
	if err := os.Mkdir(filepath.Join(binding.Workspace, ".claude"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binding.Workspace, ".claude", "settings.json"), []byte(`{"permissions":{"defaultMode":"default"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	storeDirectory := t.TempDir()
	// Each launch has its own owner and native bridge, sharing only durable files.
	launch := func(resume string, fork bool) (*claude.Client, *appServerUI, func()) {
		service, _, closeService := observationIsolationService(t, storeDirectory, binding)
		presentation, err := service.PrepareCompanion(ctx)
		if err != nil {
			t.Fatal(err)
		}
		agents := filepath.Join(presentation.Plugin, "agents")
		if err := os.Mkdir(agents, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(agents, "saved-segments.md"), []byte("---\nname: saved-segments\ndescription: Isolated saved segments acceptance.\ntools: Bash\nmodel: haiku\n---\nExecute the assigned native Bash fixture once.\n"), 0600); err != nil {
			t.Fatal(err)
		}
		helper, err := execTrackHelper()
		if err != nil {
			t.Fatal(err)
		}
		bashEnv, err := service.PrepareCommandTracking(ctx, helper, "")
		if err != nil {
			t.Fatal(err)
		}
		endpoint := service.Endpoint()
		client, err := claude.Start(ctx, "node", bridge, claude.Config{Cwd: binding.Workspace, Executable: executable, Model: "haiku", Resume: resume, ForkSession: fork, Companion: &claude.ObservationEndpoint{Socket: endpoint.Socket, Token: endpoint.Token, Plugin: presentation.Plugin, FrontendDirectory: presentation.FrontendDirectory, JournalSchema: presentation.JournalSchema, BashEnv: bashEnv}})
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
	var followupReport string
	consume := func(client *claude.Client, u *appServerUI, prompt string) {
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		done := false
		for {
			select {
			case <-ctx.Done():
				t.Fatalf("native saved segments timed out: thread=%s notice=%s", u.thread, u.notice)
			case event, open := <-client.Events():
				if !open {
					t.Fatal("native bridge closed before saved segments acceptance")
				}
				if event.Kind == "error" || event.Kind == "done" && event.Failed {
					t.Fatalf("native failure: %+v", event)
				}
				if event.Kind == "tool_result" && (event.ID == "saved-child-agent" || event.ID == "saved-child-followup-agent") && event.Failed {
					t.Fatalf("official child call failed: %s", event.Text)
				}
				if event.Kind == "tool_result" && event.ID == "saved-child-followup-agent" {
					followupReport = event.Text
				}
				if err := u.runtimeEvent(event); err != nil {
					t.Fatal(err)
				}
				if event.Kind == "prompt" {
					if event.Prompt == nil {
						t.Fatal("missing native permission")
					}
					if err := client.Respond(ctx, session.Decision{ID: event.Prompt.ID, Allow: true}); err != nil {
						t.Fatal(err)
					}
				}
				if event.Kind == "ready" {
					if prompt == "" {
						return
					}
					if err := client.Send(ctx, prompt); err != nil {
						t.Fatal(err)
					}
				}
				if event.Kind == "done" {
					done = true
				}
			case <-tick.C:
				u.flushRuntimeCommandSegments()
				if done {
					settled := 0
					mutationDone := prompt != "SAVED_PARENT_POST_FORK"
					for _, row := range u.view.entries {
						if row.CallID == "saved-child-after-fork" && row.native != nil && row.native.commandResult != nil && !row.native.running {
							mutationDone = true
						}
						id := strings.TrimPrefix(row.CallID, "history/")
						command, wanted := commands[id]
						if !wanted || row.native == nil {
							continue
						}
						parts, _ := execsegment.Split(command)
						if !row.native.running && len(row.native.segments) == len(parts) {
							settled++
						}
					}
					if settled == len(commands) && mutationDone {
						return
					}
				}
			}
		}
	}
	assertSegments := func(u *appServerUI, historical bool) {
		t.Helper()
		for id, command := range commands {
			callID := id
			if historical {
				callID = "history/" + id
			}
			var item *liveActivityNativeItem
			for _, row := range u.view.entries {
				if row.CallID == callID {
					item = row.native
				}
			}
			if item == nil {
				var ids []string
				for _, row := range u.view.entries {
					ids = append(ids, row.CallID)
				}
				t.Fatalf("missing native %s history in session %s, visible IDs=%v", callID, u.thread, ids)
			}
			caller := ""
			if id == "saved-child-bash" {
				caller = "saved-child-agent"
			}
			if item.commandResult == nil || item.commandResult.Caller != caller {
				t.Fatalf("%s lost native caller %q: %+v", callID, caller, item.commandResult)
			}
			parts, ok := execsegment.Split(command)
			if !ok || len(item.segments) != len(parts) {
				t.Fatalf("%s restored %d segments, want %d", callID, len(item.segments), len(parts))
			}
			for i, part := range item.segments {
				skipped := id == "saved-root-bash" && i == 3
				exit := 0
				if id == "saved-root-bash" && i == 2 {
					exit = 1
				}
				if part.source != parts[i].Source || part.running || part.exit != exit || part.skipped != skipped {
					t.Fatalf("%s segment %d: %+v", callID, i, part)
				}
				if skipped {
					if part.output != nil {
						t.Fatal("skipped output invented")
					}
					continue
				}
				if part.output == nil || !part.output.View().Done {
					t.Fatalf("%s segment %d output unsettled", callID, i)
				}
				want := ""
				if i == 0 {
					if id == "saved-root-bash" {
						want = "ROOT_OUTPUT"
					} else {
						want = "CHILD_OUTPUT"
					}
				}
				if id == "saved-child-bash" && i == 2 {
					want = "CHILD_FINAL"
				}
				if got := strings.Join(part.output.View().Lines, "\n"); got != want {
					t.Fatalf("%s segment %d output=%q want=%q", callID, i, got, want)
				}
			}
		}
	}
	client, u, close := launch("", false)
	consume(client, u, rootPrompt)
	assertSegments(u, false)
	u.runtime.observations.owner.mu.Lock()
	for child := range u.runtime.observations.owner.bindings {
		if child.Agent != "" {
			nativeChild = child.Agent
		}
	}
	u.runtime.observations.owner.mu.Unlock()
	if nativeChild == "" {
		t.Fatal("native child identity unavailable for official resume")
	}
	parent := u.thread
	if parent == "" {
		t.Fatal("native session identity missing")
	}
	close()
	client, u, close = launch(parent, false)
	consume(client, u, "")
	assertSegments(u, true)
	close()
	client, u, close = launch(parent, true)
	consume(client, u, "SAVED_FORK_NO_EFFECTS")
	fork := u.thread
	if fork == "" || fork == parent {
		t.Fatal("native fork identity unchanged")
	}
	assertSegments(u, true)
	close()
	// Resume the same official child after the fork, appending a new native
	// Bash result to its source transcript. Fresh fork history must stay frozen.
	mu.Lock()
	mutation = true
	mu.Unlock()
	client, u, close = launch(parent, false)
	consume(client, u, "SAVED_PARENT_POST_FORK")
	mutated := false
	for _, row := range u.view.entries {
		if row.CallID == "saved-child-after-fork" && row.native != nil && row.native.commandResult != nil && !row.native.commandResult.Failed {
			mutated = true
		}
	}
	if !mutated {
		t.Fatalf("official child resume did not execute post-fork Bash: root/child requests=%d/%d Agent result=%s", mutationRoot, mutationChild, followupReport)
	}
	close()
	client, u, close = launch(parent, false)
	consume(client, u, "")
	mutated = false
	for _, row := range u.view.entries {
		if row.CallID == "history/saved-child-after-fork" {
			mutated = true
		}
	}
	if !mutated {
		t.Fatal("official parent child history omitted the post-fork native Bash result")
	}
	close()
	assertEffects := func() {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(binding.Workspace, "effects.txt"))
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != "root\nchild\npost-fork\n" {
			t.Fatalf("native effects replayed or lost: %q", data)
		}
	}
	assertEffects()
	client, u, close = launch(fork, false)
	consume(client, u, "")
	assertSegments(u, true)
	for _, row := range u.view.entries {
		if row.CallID == "history/saved-child-after-fork" {
			t.Fatal("fresh fork borrowed post-fork source child history")
		}
	}
	close()
	assertEffects()
	mu.Lock()
	defer mu.Unlock()
	if childRequests != 2 || rootRequests < 3 || rootRequests > 6 {
		t.Fatalf("native root/child work counts=%d/%d", rootRequests, childRequests)
	}
	if mutationRoot < 2 || mutationRoot > 4 || mutationChild != 2 {
		t.Fatalf("native resumed-child work counts=%d/%d", mutationRoot, mutationChild)
	}
}
