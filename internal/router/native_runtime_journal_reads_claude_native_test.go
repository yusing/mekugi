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
	"github.com/yusing/mekugi/internal/session"
)

// Installed native MCP calls and fresh native history consume authenticated
// receipts through the shared UI, without model inference or replayed tools.
func TestNativeRuntimeJournalReadsClaudeNative(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_NATIVE_CLAUDE") != "1" {
		t.Skip("requires installed native Claude and built bridge")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	t.Setenv(routerTestWorkerEnvironment, "1")
	t.Setenv("MEKUGI_RUNTIME_DIR", t.TempDir())
	nativeGuidanceFixtureConfig(t)
	t.Setenv("ANTHROPIC_API_KEY", "native-journal-reads-fixture")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	const rootPrompt = "JOURNAL_READ_ROOT_FIXTURE"
	const childPrompt = "JOURNAL_READ_CHILD_FIXTURE"
	var mu sync.Mutex
	requests, roots, children := 0, 0, 0
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
		if requests > 18 {
			t.Error("native journal reads exceeded request budget")
			w.WriteHeader(400)
			return
		}
		content := []any{map[string]any{"type": "text", "text": "JOURNAL_READ_ACCEPTED"}}
		stop := "end_turn"
		tools, _ := packet["tools"].([]any)
		if len(tools) > 0 {
			text := nativeGuidanceRequestText(packet["messages"])
			id, name := "", ""
			var input any
			switch {
			case strings.Contains(nativeGuidanceRequestText(packet["system"]), "Use only the assigned journal tools."):
				children++
				switch children {
				case 1:
					id, name, input = "journal-child-zero", "mcp__mekugi__journal_read", map[string]any{"view": "own"}
				case 2:
					id, name, input = "journal-child-add", "mcp__mekugi__journal_batch", map[string]any{"journal": []any{map[string]any{"op": "add", "kind": "note", "title": "Native child note"}}}
				case 3:
					id, name, input = "journal-child-one", "mcp__mekugi__journal_read", map[string]any{"view": "own"}
				}
			case strings.Contains(text, rootPrompt):
				roots++
				switch roots {
				case 1:
					id, name, input = "journal-root-zero", "mcp__mekugi__journal_read", map[string]any{"view": "own"}
				case 2:
					id, name, input = "journal-root-add", "mcp__mekugi__journal_batch", map[string]any{"journal": []any{map[string]any{"op": "add", "kind": "task", "title": "Native root task", "state": "working"}, map[string]any{"op": "add", "under": "/1", "kind": "note", "title": "Nested native note"}}}
				case 3:
					id, name, input = "journal-root-two", "mcp__mekugi__journal_read", map[string]any{"view": "own"}
				case 4:
					id, name, input = "journal-root-failed", "mcp__mekugi__journal_read", map[string]any{"p": "/999"}
				case 5:
					id, name, input = "journal-reader-agent", "Agent", map[string]any{"description": "Read native child journal", "subagent_type": "mekugi:journal-reader", "prompt": childPrompt, "run_in_background": false}
				}
			default:
				t.Error("unrecognized native journal work request")
			}
			if id != "" {
				content = []any{map[string]any{"type": "tool_use", "id": id, "name": name, "input": input}}
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
	directory := t.TempDir()
	launch := func(resume string) (*claude.Client, *appServerUI, func()) {
		service, _, closeService := observationIsolationService(t, directory, binding)
		presentation, err := service.PrepareCompanion(ctx)
		if err != nil {
			t.Fatal(err)
		}
		agents := filepath.Join(presentation.Plugin, "agents")
		if err := os.Mkdir(agents, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(agents, "journal-reader.md"), []byte("---\nname: journal-reader\ndescription: Isolated native journal reader.\ntools: mcp__mekugi__journal_read, mcp__mekugi__journal_batch\nmodel: haiku\n---\nUse only the assigned journal tools.\n"), 0600); err != nil {
			t.Fatal(err)
		}
		endpoint := service.Endpoint()
		client, err := claude.Start(ctx, "node", bridge, claude.Config{Cwd: binding.Workspace, Executable: executable, Model: "haiku", Resume: resume, Companion: &claude.ObservationEndpoint{Socket: endpoint.Socket, Token: endpoint.Token, Plugin: presentation.Plugin, FrontendDirectory: presentation.FrontendDirectory, ManagedSkills: presentation.ManagedSkills, JournalSchema: presentation.JournalSchema}})
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
	outputs := make(map[string]string)
	consume := func(client *claude.Client, u *appServerUI, prompt string) {
		for {
			select {
			case <-ctx.Done():
				t.Fatalf("native journal reads timed out: thread=%s notice=%s", u.thread, u.notice)
			case event, open := <-client.Events():
				if !open {
					t.Fatal("native bridge closed before journal read acceptance")
				}
				if event.Kind == "error" || event.Kind == "done" && event.Failed || event.Kind == "tool_result" && event.Failed && strings.TrimPrefix(event.ID, "history/") != "journal-root-failed" {
					t.Fatalf("native failure: %+v", event)
				}
				if err := u.runtimeEvent(event); err != nil {
					t.Fatal(err)
				}
				if event.Kind == "tool_result" && strings.HasPrefix(event.ID, "journal-") {
					outputs[event.ID] = event.Text
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
					return
				}
			}
		}
	}
	assertReads := func(u *appServerUI, historical bool) {
		t.Helper()
		for id, want := range map[string]int{"journal-root-zero": 0, "journal-root-two": 2, "journal-root-failed": -1, "journal-child-zero": 0, "journal-child-one": 1} {
			callID := id
			if historical {
				callID = "history/" + id
			}
			var found bool
			for _, row := range u.view.entries {
				if row.CallID != callID {
					continue
				}
				found = true
				blocks := parseLiveActivity(row.activityPaneEntry)
				if len(blocks) != 1 || !blocks[0].JournalTransport || want >= 0 && (blocks[0].Results == nil || *blocks[0].Results != want) || want < 0 && (!blocks[0].Failed || blocks[0].Results != nil || blocks[0].ExitCode != 0) {
					t.Fatalf("%s lost typed read/count %d: %+v native=%+v", callID, want, blocks, row.native)
				}
				child := strings.Contains(id, "child")
				if row.native == nil || row.native.running || (row.Agent != "Main") != child {
					t.Fatalf("%s lost settled caller attribution: %+v", callID, row.native)
				}
				if row.native.output == nil || strings.Join(row.native.output.View().Lines, "\n") != outputs[id] {
					t.Fatalf("%s replaced original MCP output", callID)
				}
			}
			if !found {
				t.Fatalf("missing native journal read %s", callID)
			}
		}
	}
	client, u, close := launch("")
	consume(client, u, rootPrompt)
	assertReads(u, false)
	parent := u.thread
	if parent == "" {
		t.Fatal("native session identity missing")
	}
	close()
	mu.Lock()
	beforeRequests := requests
	mu.Unlock()
	client, u, close = launch(parent)
	consume(client, u, "")
	assertReads(u, true)
	close()
	mu.Lock()
	defer mu.Unlock()
	if requests != beforeRequests {
		t.Fatalf("native work replayed: requests=%d/%d", requests, beforeRequests)
	}
}
