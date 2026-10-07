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
	"github.com/yusing/mekugi/internal/session"
)

// Installed-native child content must reach the original roster and Activity,
// rather than a second conversation renderer or a fabricated agent transcript.
func TestNativeRuntimeAgentsClaudePTY(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_NATIVE_CLAUDE") != "1" {
		t.Skip("requires installed native Claude and built bridge")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Second)
	defer cancel()
	t.Setenv(routerTestWorkerEnvironment, "1")
	t.Setenv("MEKUGI_RUNTIME_DIR", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("ANTHROPIC_API_KEY", "native-agents-fixture")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	t.Setenv("CLAUDE_CODE_SHELL", "/bin/bash")
	service, binding, _ := observationHTTPFixture(t)
	presentation, err := service.PrepareCompanion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	agents := filepath.Join(presentation.Plugin, "agents")
	if err := os.Mkdir(agents, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agents, "view-child.md"), []byte("---\nname: view-child\ndescription: Native child viewing fixture.\ntools: Bash\nmodel: haiku\n---\nExecute the assigned fixture exactly once.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	const root = "AGENTS_ROOT_CEDAR"
	const child = "AGENTS_CHILD_CONTEXT_BIRCH"
	const follow = "AGENTS_FOLLOW_MAPLE"
	const direct = "AGENTS_DIRECT_WILLOW"
	const directed = "CHILD_ORIGINAL_CONTEXT_DIRECT"
	const initial = "CHILD_PUBLIC_BEFORE_SETTLEMENT"
	const followed = "CHILD_ORIGINAL_CONTEXT_FOLLOWUP"
	const main = "MAIN_IS_NOT_CHILD_TEXT"
	command := "printf 'once\\n' >> child-effects.txt; printf 'CHILD_BASH_LIVE\\n'; while [ ! -f child-release.gate ]; do sleep 0.05; done"
	var mu sync.Mutex
	var events []session.Event
	requests, childRequests, mainRequests := 0, 0, 0
	var followPacket, directPacket string
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
		text := nativeGuidanceRequestText(packet["messages"])
		tools, _ := packet["tools"].([]any)
		mu.Lock()
		defer mu.Unlock()
		requests++
		if requests > 22 {
			t.Error("native agents exceeded request budget")
			w.WriteHeader(400)
			return
		}
		content := []any{map[string]any{"type": "text", "text": main}}
		stop := "end_turn"
		tool := func(id, name string, input map[string]any) {
			content = []any{map[string]any{"type": "tool_use", "id": id, "name": name, "input": input}}
			stop = "tool_use"
		}
		if len(tools) > 0 {
			switch {
			case strings.Contains(text, root):
				mainRequests++
				if strings.Contains(text, direct) || strings.Contains(text, "Keep_unknown_draft") {
					t.Error("direct child command steered Main's provider context")
				}
				if strings.Contains(text, follow) && !strings.Contains(text, "agents-message") {
					tool("agents-message", "SendMessage", map[string]any{"to": nativeAgentsTaskID(events), "summary": "Continue original child", "message": follow})
				} else if !strings.Contains(text, "agents-spawn") {
					tool("agents-spawn", "Agent", map[string]any{"description": "Native child conversation", "subagent_type": "mekugi:view-child", "name": "viewchild", "prompt": child, "run_in_background": true})
				}
			case strings.Contains(text, child):
				childRequests++
				if strings.Contains(text, follow) {
					followPacket = text
					if !strings.Contains(text, "agents-follow-bash") {
						tool("agents-follow-bash", "Bash", map[string]any{"command": "printf 'CHILD_FOLLOW_LIVE\\n'; while [ ! -f child-stop.gate ]; do sleep 0.05; done", "description": "Child follow-up gated stop"})
						content = append([]any{map[string]any{"type": "text", "text": followed}}, content...)
					} else {
						content = []any{map[string]any{"type": "text", "text": "CHILD_FOLLOW_SETTLED"}}
					}
				} else if strings.Contains(text, direct) {
					directPacket = text
					content = []any{map[string]any{"type": "text", "text": directed}}
				} else if childRequests == 1 {
					tool("agents-child-bash", "Bash", map[string]any{"command": command, "description": "Child gated effect"})
					content = append([]any{map[string]any{"type": "text", "text": initial}}, content...)
				} else {
					content = []any{map[string]any{"type": "text", "text": "CHILD_SETTLED"}}
				}
			}
		}
		recorded := httptest.NewRecorder()
		nativeGuidanceProviderReply(recorded, packet, content, stop)
		for k, values := range recorded.Header() {
			w.Header()[k] = values
		}
		w.WriteHeader(recorded.Code)
		_, _ = fmt.Fprint(w, strings.ReplaceAll(recorded.Body.String(), "fixture-native-recovery", fmt.Sprintf("fixture-native-agents-%d", requests)))
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
	endpoint := service.Endpoint()
	client, err := claude.Start(ctx, "node", bridge, claude.Config{Cwd: binding.Workspace, Executable: executable, Model: "haiku", Companion: &claude.ObservationEndpoint{Socket: endpoint.Socket, Token: endpoint.Token, Plugin: presentation.Plugin, FrontendDirectory: presentation.FrontendDirectory, JournalSchema: presentation.JournalSchema}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	observed := &nativeClaudePTYClient{Client: client, events: make(chan session.Event)}
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		defer close(observed.events)
		for {
			select {
			case <-ctx.Done():
				return
			case e, ok := <-client.Events():
				if !ok {
					return
				}
				mu.Lock()
				events = append(events, e)
				mu.Unlock()
				select {
				case observed.events <- e:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	terminal, stop := startNativeRuntimePTY(t, ctx, observed, binding.Workspace, service)
	t.Cleanup(func() {
		_ = os.WriteFile(filepath.Join(binding.Workspace, "child-release.gate"), nil, 0600)
		_ = os.WriteFile(filepath.Join(binding.Workspace, "child-stop.gate"), nil, 0600)
		cancel()
		stop()
		<-joined
	})
	await := func(label string, match func(string) bool) {
		t.Helper()
		if !terminal.observe(25*time.Second, func(s string) bool {
			if strings.Contains(s, "Permission · Bash") && strings.Contains(s, "Allow once") {
				terminal.keys("1\r")
			}
			return match(s)
		}) {
			mu.Lock()
			for _, e := range events {
				t.Logf("native event: kind=%s role=%s id=%s caller=%s text=%s task=%+v", e.Kind, e.Role, e.ID, e.Caller, e.Text, e.Task)
			}
			mu.Unlock()
			t.Fatalf("missing %s:\n%s", label, terminal.screen.String())
		}
	}
	click := func(target string, bottom bool) {
		t.Helper()
		rows := strings.Split(terminal.screen.String(), "\n")
		for y, row := range rows {
			if bottom && y < 31 {
				continue
			}
			runes := []rune(row)
			offset := 0
			if !bottom {
				offset = 60
			}
			if len(runes) <= offset {
				continue
			}
			visible := string(runes[offset:])
			if at := strings.Index(visible, target); at >= 0 {
				x := offset + len([]rune(visible[:at])) + 1
				terminal.keys(fmt.Sprintf("\x1b[<0;%d;%dM\x1b[<0;%d;%dm", x, y+1, x, y+1))
				return
			}
		}
		t.Fatalf("missing click target %q:\n%s", target, terminal.screen.String())
	}
	await("ready", func(s string) bool { return strings.Contains(s, "Ready") })
	terminal.paste(root)
	await("root settled while child runs", func(s string) bool {
		return strings.Contains(s, main) && strings.Contains(s, "Ready") && strings.Contains(s, "running")
	})
	terminal.keys("\x02" + "4")
	terminal.keys("\x1b[B\r")
	await("original child Activity before settlement", func(s string) bool {
		return strings.Contains(nativeAgentsActivity(s), initial) && !strings.Contains(nativeAgentsActivity(s), main) && strings.Contains(s, "running")
	})
	mu.Lock()
	var childTask string
	var childText bool
	for _, e := range events {
		if e.Kind == "task" && e.Task != nil && e.Task.ToolID == "agents-spawn" {
			childTask = e.Task.ID
		}
		if e.Kind == "message" && e.Caller == "agents-spawn" && strings.Contains(e.Text, initial) {
			childText = true
		}
	}
	mu.Unlock()
	if childTask == "" || !childText {
		t.Fatalf("child text lost native task/caller identity: task=%q text=%v", childTask, childText)
	}
	await("child Bash output before settlement", func(s string) bool { return strings.Contains(nativeAgentsActivity(s), "┆ CHILD_BASH_LIVE") })
	click("┆ CHILD_BASH_LIVE", false)
	await("original shared child output dialog", func(s string) bool {
		return strings.Contains(s, "y copy · esc") && strings.Contains(s, "1 ┆ CHILD_BASH_LIVE") && strings.Contains(s, "● live")
	})
	terminal.keys("\x1b")
	await("child dialog closed", func(s string) bool { return !strings.Contains(s, "y copy · esc") })
	// Selecting Main restores all children; Main text remains in Main.
	// Selecting the child restores that child's original Activity filter.
	click("main", true)
	await("Main filter", func(s string) bool {
		return strings.Contains(nativeAgentsActivity(s), "Agent Native child conversation") && !strings.Contains(s, "Activity · "+childTask)
	})
	click(childTask, true)
	await("mouse child filter", func(s string) bool {
		return strings.Contains(nativeAgentsActivity(s), initial) && !strings.Contains(nativeAgentsActivity(s), main)
	})
	// Direct composer controls deliver to the busy native child without a Main
	// prompt. Completion inserts the roster's original native identity.
	terminal.keys("\x02" + "1")
	mu.Lock()
	beforeDirect := mainRequests
	mu.Unlock()
	terminal.keys("/to\t")
	await("native child target completion", func(s string) bool {
		return strings.Contains(s, "/root/"+childTask) && strings.Contains(s, "/to ")
	})
	t.Logf("direct target completion frame:\n%s", terminal.screen.String())
	terminal.keys("\t")
	await("native child target inserted", func(s string) bool { return strings.Contains(s, "/to /root/"+childTask+" ") })
	terminal.keys(direct + "\r")
	await("busy child direct queue receipt", func(s string) bool {
		return strings.Contains(s, "Native child message queued") && strings.Contains(s, "Ready") && strings.Contains(s, "running")
	})
	t.Logf("busy direct delivery frame:\n%s", terminal.screen.String())
	mu.Lock()
	if mainRequests != beforeDirect || directPacket != "" {
		t.Errorf("queued busy-child message steered Main or passed the Bash gate: main=%d want=%d child=%q", mainRequests, beforeDirect, directPacket)
	}
	mu.Unlock()
	terminal.keys("\x02" + "4")
	await("directed child Activity receipt", func(s string) bool {
		return strings.Contains(s, "Activity · "+childTask) && strings.Contains(nativeAgentsActivity(s), "You via Mekugi") && strings.Contains(nativeAgentsActivity(s), direct)
	})
	if err := os.WriteFile(filepath.Join(binding.Workspace, "child-release.gate"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	await("same child direct continuation", func(s string) bool {
		return strings.Contains(s, "Activity · "+childTask) && strings.Contains(nativeAgentsActivity(s), directed)
	})
	t.Logf("direct child continuation frame:\n%s", terminal.screen.String())
	mu.Lock()
	directContext := strings.Contains(directPacket, child) && strings.Contains(directPacket, initial) && strings.Contains(directPacket, "agents-child-bash")
	if !directContext {
		t.Error("direct continuation lost original child context")
	}
	mu.Unlock()
	terminal.keys("\x02" + "1")
	const rejectedDraft = "/to missing-native-child Keep_unknown_draft"
	terminal.paste(rejectedDraft)
	await("unknown native target draft retained", func(s string) bool {
		return strings.Contains(s, "Native child message unavailable") && strings.Contains(s, "/to missing-native-child Keep_unknown_draft")
	})
	t.Logf("rejected target draft frame:\n%s", terminal.screen.String())
	// Clear only the failed draft, then retain ordinary Main/model-tool delivery.
	terminal.keys(strings.Repeat("\x7f", len(rejectedDraft)))
	terminal.paste(follow)
	await("native model-tool message receipt", func(s string) bool { return strings.Contains(s, "Ready") && strings.Contains(s, "SendMessage") })
	terminal.keys("\x02" + "4")
	await("same child follow-up text", func(s string) bool {
		return strings.Contains(s, "Activity · "+childTask) && strings.Contains(nativeAgentsActivity(s), followed) && strings.Contains(s, "Child follow-up gated stop")
	})
	terminal.keys("x")
	await("native child stop receipt", func(s string) bool { return strings.Contains(s, "stopped") || strings.Contains(s, "killed") })
	mu.Lock()
	settled, followText, directText := false, false, false
	terminalStatus := ""
	for _, e := range events {
		if e.Kind == "task" && e.Task != nil && e.Task.ID == childTask && (e.Task.Status == "stopped" || e.Task.Status == "killed") {
			settled = true
			terminalStatus = e.Task.Status
		}
		if e.Kind == "message" && e.Caller == "agents-spawn" && strings.Contains(e.Text, followed) {
			followText = true
		}
		if e.Kind == "message" && e.Caller == "agents-spawn" && strings.Contains(e.Text, directed) {
			directText = true
		}
	}
	contextSeen := strings.Contains(followPacket, child) && strings.Contains(followPacket, initial) && strings.Contains(followPacket, "agents-child-bash")
	mu.Unlock()
	if !settled {
		t.Fatal("child stop displayed without native terminal task receipt")
	}
	if !contextSeen || !followText {
		t.Fatal("native SendMessage did not preserve original child context")
	}
	if !directText {
		t.Fatal("direct child response lost original native caller identity")
	}
	effect, err := os.ReadFile(filepath.Join(binding.Workspace, "child-effects.txt"))
	if err != nil || string(effect) != "once\n" {
		t.Fatalf("child effect: %q %v", effect, err)
	}
	t.Logf("native child=%s caller=agents-spawn status=%s; direct autocomplete/queue/context, rejected draft, model-tool follow-up and exactly-once Bash effect verified", childTask, terminalStatus)
}

func nativeAgentsTaskID(events []session.Event) string {
	for _, e := range events {
		if e.Task != nil && e.Task.ToolID == "agents-spawn" {
			return e.Task.ID
		}
	}
	return "missing-native-child"
}

func nativeAgentsActivity(s string) string {
	var rows []string
	for y, row := range strings.Split(s, "\n") {
		if y >= 31 {
			break
		}
		r := []rune(row)
		if len(r) > 60 {
			rows = append(rows, string(r[60:]))
		}
	}
	return strings.Join(rows, "\n")
}
