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
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/claude"
	"github.com/yusing/mekugi/internal/session"
)

// Installed Claude executes every load. Fresh bridges restore only native
// history, including the official fork's inherited child message selection.
func TestNativeRuntimeSavedSkillsClaudeNative(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_NATIVE_CLAUDE") != "1" {
		t.Skip("requires installed native Claude and built bridge")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 110*time.Second)
	defer cancel()
	t.Setenv(routerTestWorkerEnvironment, "1")
	t.Setenv("MEKUGI_RUNTIME_DIR", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("ANTHROPIC_API_KEY", "native-saved-skills-fixture")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	binding := ObservationBinding{Runtime: "claude", Workspace: t.TempDir()}
	if err := os.Mkdir(filepath.Join(binding.Workspace, ".claude"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binding.Workspace, ".claude", "settings.json"), []byte(`{"permissions":{"defaultMode":"default"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	storeDirectory := t.TempDir()
	paths := make(map[string]string)
	for _, name := range []string{"root-read", "child-read", "child-later"} {
		dir := filepath.Join(binding.Workspace, name)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		paths[name] = filepath.Join(dir, "SKILL.md")
		if err := os.WriteFile(paths[name], []byte("---\nname: "+name+"\ndescription: Native saved skill fixture.\n---\nRetain this loaded skill in the current context.\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	const rootPrompt = "SAVED_SKILLS_ROOT"
	const childPrompt = "SAVED_SKILLS_CHILD"
	const followupPrompt = "SAVED_SKILLS_PARENT_FOLLOWUP"
	const childFollowup = "SAVED_SKILLS_CHILD_FOLLOWUP"
	var mu sync.Mutex
	counts := make(map[string]int)
	requests := 0
	phase := "loads"
	nativeChild := ""
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
		if requests > 32 {
			t.Error("native saved skills exceeded request budget")
			w.WriteHeader(400)
			return
		}
		content := []any{map[string]any{"type": "text", "text": "SAVED_SKILLS_ACCEPTED"}}
		stop := "end_turn"
		tools, _ := packet["tools"].([]any)
		if len(tools) > 0 {
			text := nativeGuidanceRequestText(packet["messages"])
			tool := func(id, name string, input map[string]any) {
				content = []any{map[string]any{"type": "tool_use", "id": id, "name": name, "input": input}}
				stop = "tool_use"
			}
			read := func(id, name string) { tool(id, "Read", map[string]any{"file_path": paths[name]}) }
			agent := func(id, prompt, resume string) {
				input := map[string]any{"description": "Native saved skills", "subagent_type": "mekugi:saved-skills", "prompt": prompt, "run_in_background": false}
				if resume != "" {
					input["resume"] = resume
				}
				tool(id, "Agent", input)
			}
			switch {
			case phase == "compact":
				counts["summary"]++
				content = []any{map[string]any{"type": "text", "text": "NATIVE_SKILLS_COMPACT_SUMMARY: prior work completed. No skill contents retained."}}
			case phase == "followup" && strings.Contains(text, followupPrompt):
				counts["parent-followup"]++
				if counts["parent-followup"] == 1 {
					agent("saved-child-followup", childFollowup, nativeChild)
				}
			case phase == "followup" && strings.Contains(text, childFollowup):
				counts["child-followup"]++
				if counts["child-followup"] == 1 {
					read("saved-child-later", "child-later")
				}
			case phase == "loads" && strings.Contains(text, rootPrompt):
				counts["root"]++
				switch counts["root"] {
				case 1:
					tool("saved-root-skill", "Skill", map[string]any{"skill": "mekugi:saved-canonical"})
				case 2:
					read("saved-root-read", "root-read")
				case 3:
					agent("saved-child-agent", childPrompt, "")
				}
			case phase == "loads" && strings.Contains(text, childPrompt):
				counts["child"]++
				if counts["child"] == 1 {
					read("saved-child-read", "child-read")
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
	bridge, err := filepath.Abs("../claude/bridge/dist/bridge.js")
	if err != nil {
		t.Fatal(err)
	}
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
		if err := os.WriteFile(filepath.Join(agents, "saved-skills.md"), []byte("---\nname: saved-skills\ndescription: Isolated native skill history acceptance.\ntools: Read\nmodel: haiku\n---\nRead only the assigned skill once.\n"), 0600); err != nil {
			t.Fatal(err)
		}
		skill := filepath.Join(presentation.Plugin, "skills", "saved-canonical")
		if err := os.Mkdir(skill, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("---\nname: saved-canonical\ndescription: Isolated canonical native Skill acceptance.\n---\nThis canonical skill has loaded. Continue the assigned fixture.\n"), 0600); err != nil {
			t.Fatal(err)
		}
		endpoint := service.Endpoint()
		client, err := claude.Start(ctx, "node", bridge, claude.Config{Cwd: binding.Workspace, Executable: executable, Model: "haiku", Resume: resume, ForkSession: fork, Companion: &claude.ObservationEndpoint{Socket: endpoint.Socket, Token: endpoint.Token, Plugin: presentation.Plugin, FrontendDirectory: presentation.FrontendDirectory, JournalSchema: presentation.JournalSchema}})
		if err != nil {
			t.Fatal(err)
		}
		u := newRuntimeUI(ctx, client, "Claude Code", binding.Workspace)
		u.attachRuntimeObservation(service)
		close := sync.OnceFunc(func() { client.Close(); u.shell.diff.close(); u.shell.diffScreen.Close(); closeService() })
		t.Cleanup(close)
		return client, u, close
	}
	results := make(map[string]int)
	canonical := ""
	laterChild := ""
	consume := func(client *claude.Client, u *appServerUI, prompt string, fresh bool) {
		t.Helper()
		if !fresh {
			if err := client.Send(ctx, prompt); err != nil {
				t.Fatal(err)
			}
		}
		for {
			select {
			case <-ctx.Done():
				t.Fatalf("native saved skills timed out: session=%s notice=%s", u.thread, u.notice)
			case event, open := <-client.Events():
				if !open {
					t.Fatal("native bridge closed before saved skill acceptance")
				}
				if event.Kind == "error" || event.Kind == "done" && event.Failed || event.Kind == "skill_history" && event.Failed {
					t.Fatalf("native failure: %+v", event)
				}
				if event.Kind == "tool_result" {
					if event.Failed {
						t.Fatalf("native tool %s failed: %s", event.ID, event.Text)
					}
					results[event.ID]++
					if event.ID == "saved-root-skill" {
						canonical = event.Skill
						if canonical == "" {
							t.Fatalf("native canonical Skill receipt was not decoded: %s", event.Text)
						}
					}
				}
				if err := u.runtimeEvent(event); err != nil {
					t.Fatal(err)
				}
				if event.Kind == "prompt" {
					if event.Prompt == nil {
						t.Fatal("missing native permission prompt")
					}
					if err := client.Respond(ctx, session.Decision{ID: event.Prompt.ID, Allow: true}); err != nil {
						t.Fatal(err)
					}
				}
				if fresh && event.Kind == "ready" {
					if prompt == "" {
						return
					}
					if err := client.Send(ctx, prompt); err != nil {
						t.Fatal(err)
					}
				}
				if event.Kind == "done" {
					return
				}
			}
		}
	}
	assertSkills := func(u *appServerUI, main, child []string) {
		t.Helper()
		for _, view := range []*liveActivityView{u.view, u.agents} {
			wanted := map[string][]string{"Main": main, "/root/" + nativeChild: child}
			if laterChild != "" {
				wanted["/root/"+laterChild] = []string{"child-later"}
			}
			for owner, want := range wanted {
				got := slices.Clone(view.activeSkills()[owner])
				slices.Sort(got)
				want = slices.Clone(want)
				slices.Sort(want)
				if !slices.Equal(got, want) {
					mu.Lock()
					defer mu.Unlock()
					t.Fatalf("session=%s owner=%s skills=%v want=%v all=%v results=%v requests=%v notice=%s", u.thread, owner, got, want, view.activeSkills(), results, counts, u.notice)
				}
			}
		}
	}
	client, u, close := launch("", false)
	consume(client, u, rootPrompt, true)
	u.runtime.observations.owner.mu.Lock()
	for child := range u.runtime.observations.owner.bindings {
		if child.Agent != "" {
			nativeChild = child.Agent
		}
	}
	u.runtime.observations.owner.mu.Unlock()
	if nativeChild == "" || u.thread == "" {
		t.Fatal("native parent/child identity missing")
	}
	if canonical != "mekugi:saved-canonical" {
		t.Fatalf("canonical native skill=%q", canonical)
	}
	main := []string{canonical, "root-read"}
	child := []string{"child-read"}
	assertSkills(u, main, child)
	parent := u.thread
	close()
	client, u, close = launch(parent, false)
	consume(client, u, "", true)
	assertSkills(u, main, child)
	close()
	// A real native query creates the fork. Observation alone cannot create it.
	mu.Lock()
	phase = "fork"
	mu.Unlock()
	client, u, close = launch(parent, true)
	consume(client, u, "SAVED_SKILLS_FORK_NO_LOADS", true)
	fork := u.thread
	if fork == "" || fork == parent {
		t.Fatal("official native fork identity unchanged")
	}
	assertSkills(u, main, child)
	close()
	mu.Lock()
	phase = "followup"
	mu.Unlock()
	client, u, close = launch(parent, false)
	consume(client, u, followupPrompt, true)
	if results["saved-child-later"] != 1 {
		t.Fatal("official child resume did not execute its new Read")
	}
	for owner, names := range u.agents.activeSkills() {
		if slices.Contains(names, "child-later") {
			if laterChild != "" || owner == "Main" {
				t.Fatal("resumed native child load has ambiguous ownership")
			}
			laterChild = strings.TrimPrefix(owner, "/root/")
		}
	}
	if laterChild == "" || laterChild == nativeChild {
		t.Fatal("native resumed child did not publish its independent identity")
	}
	assertSkills(u, main, child)
	close()
	client, u, close = launch(parent, false)
	consume(client, u, "", true)
	assertSkills(u, main, child)
	close()
	client, u, close = launch(fork, false)
	consume(client, u, "", true)
	resumedChild := laterChild
	laterChild = ""
	assertSkills(u, main, []string{"child-read"})
	for _, names := range u.agents.activeSkills() {
		if slices.Contains(names, "child-later") {
			t.Fatal("fresh fork borrowed post-fork source child history")
		}
	}
	close()
	laterChild = resumedChild
	// Native classic compaction expires only Main, retaining independent child context.
	mu.Lock()
	phase = "compact"
	mu.Unlock()
	client, u, close = launch(parent, false)
	consume(client, u, "", true)
	consume(client, u, "/compact", false)
	assertSkills(u, nil, child)
	close()
	client, u, close = launch(parent, false)
	consume(client, u, "", true)
	assertSkills(u, nil, child)
	close()
	for _, id := range []string{"saved-root-skill", "saved-root-read", "saved-child-agent", "saved-child-read", "saved-child-followup", "saved-child-later"} {
		if results[id] != 1 {
			t.Fatalf("native tool %s executed/replayed %d times", id, results[id])
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if counts["summary"] == 0 {
		t.Fatal("native compaction did not use the scripted summary")
	}
	t.Logf("native Skill + Read, independent child, fresh resume, official fork selection and compact restoration; %d local requests, no inference", requests)
	t.Logf("official child resume created native identity %s from %s; fresh native chains independently retain child-later and child-read", resumedChild, nativeChild)
}
