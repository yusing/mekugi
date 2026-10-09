//go:build unix

package router

import (
	"context"
	"encoding/json/v2"
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

// Actual native child input and saved native history feed the same shared PTY.
// The child SDK currently exposes completed input, not partial input streams.
func TestNativeRuntimePreviewContinuityClaudePTY(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_NATIVE_CLAUDE") != "1" {
		t.Skip("requires installed native Claude and built bridge")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	t.Setenv(routerTestWorkerEnvironment, "1")
	t.Setenv("MEKUGI_RUNTIME_DIR", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	// Local providers exercise native prompts without an inference-based Auto classifier.
	if err := os.WriteFile(filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), "settings.json"), []byte(`{"permissions":{"defaultMode":"default"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ANTHROPIC_API_KEY", "native-preview-continuity-fixture")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	t.Setenv("CLAUDE_CODE_SHELL", "/bin/bash")
	binding := ObservationBinding{Runtime: "claude", Workspace: t.TempDir()}
	directory := t.TempDir()
	const root = "PREVIEW_CONTINUITY_ROOT"
	const child = "PREVIEW_CONTINUITY_CHILD"
	const output = "CHILD_PREVIEW_OUTPUT"
	command := "cat > child-proposal.txt <<'EOF'\nCHILD_PROPOSAL_CEDAR\nEOF\nprintf 'CHILD_PREVIEW_OUTPUT\\n'; while [ ! -f child-finish.gate ]; do sleep 0.05; done; printf 'once\\n' >> child-effects.txt"
	var mu sync.Mutex
	rootRequests, childRequests, requests := 0, 0, 0
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
			t.Error("native preview continuity exceeded request budget")
			w.WriteHeader(400)
			return
		}
		content := []any{map[string]any{"type": "text", "text": "CONTINUITY_ROOT_COMPLETE"}}
		stop := "end_turn"
		tools, _ := packet["tools"].([]any)
		text := nativeGuidanceRequestText(packet["messages"])
		if len(tools) > 0 && !strings.Contains(text, "CONTINUITY_FORK_NO_EFFECTS") {
			switch {
			case strings.Contains(text, root):
				rootRequests++
				if rootRequests == 1 {
					content = []any{map[string]any{"type": "tool_use", "id": "continuity-spawn", "name": "Agent", "input": map[string]any{"description": "Preview continuity child", "subagent_type": "mekugi:preview-child", "prompt": child, "run_in_background": true}}}
					stop = "tool_use"
				}
			case strings.Contains(text, child):
				childRequests++
				if childRequests == 1 {
					content = []any{map[string]any{"type": "tool_use", "id": "continuity-child-bash", "name": "Bash", "input": map[string]any{"command": command, "description": "Child proposal command"}}}
					stop = "tool_use"
				} else {
					content = []any{map[string]any{"type": "text", "text": "CONTINUITY_CHILD_COMPLETE"}}
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
	var events []session.Event
	launch := func(resume string, fork bool) (*nativeClaudePTY, *ObservationService, func()) {
		t.Helper()
		service, _, closeService := observationIsolationService(t, directory, binding)
		presentation, err := service.PrepareCompanion(ctx)
		if err != nil {
			t.Fatal(err)
		}
		agents := filepath.Join(presentation.Plugin, "agents")
		if err := os.Mkdir(agents, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(agents, "preview-child.md"), []byte("---\nname: preview-child\ndescription: Native preview continuity fixture.\ntools: Bash\nmodel: haiku\n---\nExecute the assigned command exactly once.\n"), 0600); err != nil {
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
		forwardCtx, stopForward := context.WithCancel(ctx)
		observed := &nativeClaudePTYClient{Client: client, events: make(chan session.Event)}
		joined := make(chan struct{})
		go func() {
			defer close(joined)
			defer close(observed.events)
			for {
				select {
				case <-forwardCtx.Done():
					return
				case event, ok := <-client.Events():
					if !ok {
						return
					}
					mu.Lock()
					events = append(events, event)
					mu.Unlock()
					select {
					case observed.events <- event:
					case <-forwardCtx.Done():
						return
					}
				}
			}
		}()
		terminal, stopTerminal := startNativeRuntimePTY(t, ctx, observed, binding.Workspace, service)
		close := sync.OnceFunc(func() { stopTerminal(); stopForward(); client.Close(); <-joined; closeService() })
		t.Cleanup(close)
		return terminal, service, close
	}
	var terminal *nativeClaudePTY
	await := func(label string, match func(string) bool) {
		t.Helper()
		if !terminal.observe(25*time.Second, func(frame string) bool {
			if strings.Contains(frame, "Permission · Bash") && strings.Contains(frame, "Allow once") {
				terminal.keys("1\r")
			}
			return match(frame)
		}) {
			t.Fatalf("missing %s:\n%s", label, terminal.screen.String())
		}
	}
	click := func(target string) {
		t.Helper()
		// Click the selected child's Activity column, not concurrently changing Main.
		for y, row := range strings.Split(terminal.screen.String(), "\n") {
			runes := []rune(row)
			if y >= 31 || len(runes) <= 60 {
				continue
			}
			visible := string(runes[60:])
			if at := strings.Index(visible, target); at >= 0 {
				x := 61 + len([]rune(visible[:at]))
				terminal.keys(fmt.Sprintf("\x1b[<0;%d;%dM\x1b[<0;%d;%dm", x, y+1, x, y+1))
				return
			}
		}
		t.Fatalf("missing child Activity click target %q:\n%s", target, terminal.screen.String())
	}
	selectChild := func() {
		t.Helper()
		terminal.keys("\x02" + "4")
		terminal.keys("\x1b[B\r")
		await("native child selection", func(s string) bool {
			return strings.Contains(nativeAgentsActivity(s), "┆ "+output) && !strings.Contains(nativeAgentsActivity(s), "CONTINUITY_ROOT_COMPLETE")
		})
	}
	terminal, service, closeParent := launch("", false)
	t.Cleanup(func() { _ = os.WriteFile(filepath.Join(binding.Workspace, "child-finish.gate"), nil, 0600) })
	await("ready", func(s string) bool { return strings.Contains(s, "Ready") })
	terminal.paste(root)
	await("Main settled with running native child", func(s string) bool {
		return strings.Contains(s, "CONTINUITY_ROOT_COMPLETE") && strings.Contains(s, "Ready") && strings.Contains(s, "running")
	})
	await("native child output before selection", func(s string) bool {
		return strings.Contains(s, "┆ "+output) && !strings.Contains(s, "Permission · Bash")
	})
	selectChild()
	click("┆ " + output)
	await("completed child segment while native child runs", func(s string) bool {
		return strings.Contains(s, "y copy · esc") && strings.Contains(s, "1 ┆ "+output) && strings.Contains(s, "exit 0") && !strings.Contains(s, "● live")
	})
	terminal.keys("\x1b")
	await("child output dialog closed", func(s string) bool { return !strings.Contains(s, "y copy · esc") })
	terminal.keys("\x02" + "2")
	terminal.keys("v")
	await("native child completed-input proposal", func(s string) bool {
		return strings.Contains(nativeClaudeDiffPane(s), "CHILD_PROPOSAL_CEDAR") && strings.Contains(nativeClaudeDiffPane(s), "live proposals")
	})
	mu.Lock()
	parent, childTask := "", ""
	completeChildInput, partialChildInput := false, false
	for _, event := range events {
		if event.SessionID != "" {
			parent = event.SessionID
		}
		if event.Task != nil && event.Task.ToolID == "continuity-spawn" {
			childTask = event.Task.ID
		}
		if event.Kind == "command_preview" && event.ID == "continuity-child-bash" && event.Caller == "continuity-spawn" && event.CommandInput != nil {
			completeChildInput = completeChildInput || event.CommandInput.Complete && event.CommandInput.Text == command
			partialChildInput = partialChildInput || !event.CommandInput.Complete
		}
	}
	mu.Unlock()
	if parent == "" || childTask == "" || !completeChildInput {
		t.Fatalf("native child input/caller evidence: session=%q child=%q complete=%v", parent, childTask, completeChildInput)
	}
	if !strings.Contains(nativeClaudeDiffPane(terminal.screen.String()), childTask) {
		t.Fatalf("child proposal lost shared task caller %q:\n%s", childTask, nativeClaudeDiffPane(terminal.screen.String()))
	}
	t.Logf("installed native child input: complete=%v partial=%v", completeChildInput, partialChildInput)
	if err := os.WriteFile(filepath.Join(binding.Workspace, "child-finish.gate"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	terminal.keys("\x02" + "3")
	await("child native terminal completion", func(s string) bool {
		return strings.Count(nativeAgentsActivity(s), "CONTINUITY_CHILD_COMPLETE") >= 2 && strings.Contains(s, "Captured") && strings.Contains(s, "Child proposal command · completed") && !strings.Contains(s, "running")
	})
	click("┆ " + output)
	await("settled child segment dialog", func(s string) bool {
		return strings.Contains(s, "y copy · esc") && strings.Contains(s, "1 ┆ "+output) && !strings.Contains(s, "● live")
	})
	terminal.keys("\x1b")
	await("settled child dialog closed", func(s string) bool { return !strings.Contains(s, "y copy · esc") })
	click("Child proposal command · completed")
	await("child native Events dialog", func(s string) bool {
		return strings.Contains(s, "y copy · esc") && strings.Contains(s, "Child proposal command · completed") && strings.Contains(s, "CONTINUITY_CHILD_COMPLETE")
	})
	terminal.keys("\x1b")
	await("child Events dialog closed", func(s string) bool { return !strings.Contains(s, "y copy · esc") })
	mu.Lock()
	results := 0
	for _, event := range events {
		if event.Kind == "tool_result" && event.ID == "continuity-child-bash" {
			results++
			if event.Caller != "continuity-spawn" || event.Failed || !strings.Contains(event.Text, output) {
				t.Errorf("native child command receipt: %+v", event)
			}
		}
	}
	mu.Unlock()
	if results != 1 {
		t.Fatalf("native child terminal results=%d", results)
	}
	actual, err := os.ReadFile(filepath.Join(binding.Workspace, "child-proposal.txt"))
	if err != nil || string(actual) != "CHILD_PROPOSAL_CEDAR\n" {
		t.Fatalf("native child proposal effects: %q %v", actual, err)
	}
	service.owner.mu.Lock()
	sourceScopes := map[string]bool{}
	for observedBinding := range service.owner.bindings {
		if observedBinding.Session == parent {
			sourceScopes[observationThread(observedBinding)] = true
		}
	}
	service.owner.mu.Unlock()
	files, err := service.owner.store.liveDiffSnapshotFiles(ctx, liveDiffScope{Workspaces: map[string]map[string]bool{binding.Workspace: sourceScopes}})
	if err != nil {
		t.Fatal(err)
	}
	savedTargets := map[string]bool{}
	for _, file := range files {
		if len(file.Chunks) > 0 && file.Chunks[0].Change != "" {
			savedTargets[filepath.Base(file.Path)] = true
		}
	}
	if !savedTargets["child-proposal.txt"] || !savedTargets["child-effects.txt"] {
		t.Fatalf("source child targets not captured: %v", savedTargets)
	}
	// Set up the selected task through the shared journal owner. Native hooks
	// alone must establish the child identity used by this mount and its captures.
	rootBinding := service.journal.rootBinding()
	rootCtx, err := service.journal.scope(ctx, rootBinding)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.journal.journals.apply(rootCtx, service.owner.store, binding.Workspace, observationThread(rootBinding), "fixture-child-mount",
		[]journalMutation{{Op: "add", Kind: "task", Title: new("Selected child"), State: new("working"), Agent: "/root/" + childTask}}); err != nil {
		t.Fatal(err)
	}
	assertMountedDiff := func() {
		t.Helper()
		terminal.keys("\x02" + "2")
		await("mounted native child saved Diff", func(s string) bool {
			pane := nativeClaudeDiffPane(s)
			return strings.Contains(pane, "subslice /1") && strings.Contains(pane, "child-proposal.txt") && strings.Contains(pane, "CHILD_PROPOSAL_CEDAR")
		})
	}
	terminal.keys("\x02" + "2" + "v") // Return from proposals to saved Diff.
	assertMountedDiff()
	closeParent()
	assertSaved := func() {
		t.Helper()
		selectChild()
		click("┆ " + output)
		await("fresh saved child segment dialog", func(s string) bool {
			return strings.Contains(s, "y copy · esc") && strings.Contains(s, "1 ┆ "+output) && !strings.Contains(s, "● live")
		})
		terminal.keys("\x1b")
		await("saved child dialog closed", func(s string) bool { return !strings.Contains(s, "y copy · esc") })
	}
	mu.Lock()
	beforeRequests := requests
	events = nil
	mu.Unlock()
	terminal, _, closeResume := launch(parent, false)
	await("fresh parent native history", func(s string) bool {
		return strings.Contains(s, "Ready") && strings.Contains(s, "CONTINUITY_ROOT_COMPLETE")
	})
	assertMountedDiff()
	assertSaved()
	closeResume()
	mu.Lock()
	if requests != beforeRequests {
		t.Error("fresh parent resume requested provider work")
	}
	for _, event := range events {
		if event.Kind == "command_preview" {
			t.Error("history revived live child input")
		}
	}
	events = nil
	mu.Unlock()
	terminal, forkService, closeFork := launch(parent, true)
	await("fork native ready", func(s string) bool { return strings.Contains(s, "Ready") })
	terminal.paste("CONTINUITY_FORK_NO_EFFECTS")
	await("fork query settlement", func(s string) bool {
		return strings.Contains(s, "Ready") && strings.Contains(s, "CONTINUITY_FORK_NO_EFFECTS") && strings.Contains(s, "CONTINUITY_ROOT_COMPLETE")
	})
	assertSaved()
	mu.Lock()
	forkID := ""
	for _, event := range events {
		if event.SessionID != "" {
			forkID = event.SessionID
		}
	}
	mu.Unlock()
	if forkID == "" || forkID == parent {
		t.Fatal("native fork identity unchanged")
	}
	files, err = forkService.owner.store.liveDiffSnapshotFiles(ctx, liveDiffScope{Workspaces: map[string]map[string]bool{binding.Workspace: {observationThread(ObservationBinding{Runtime: "claude", Workspace: binding.Workspace, Session: forkID}): true}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatal("fork borrowed parent saved Diff capture authority")
	}
	closeFork()
	mu.Lock()
	beforeRequests = requests
	events = nil
	mu.Unlock()
	terminal, _, closeFreshFork := launch(forkID, false)
	await("fresh fork ready", func(s string) bool {
		return strings.Contains(s, "Ready") && strings.Contains(s, "CONTINUITY_FORK_NO_EFFECTS")
	})
	assertSaved()
	closeFreshFork()
	mu.Lock()
	defer mu.Unlock()
	if requests != beforeRequests || childRequests != 2 {
		t.Fatalf("native work repeated: before=%d after=%d child=%d", beforeRequests, requests, childRequests)
	}
	for _, event := range events {
		if event.Kind == "command_preview" {
			t.Error("fresh fork history revived live preview input")
		}
	}
	effects, err := os.ReadFile(filepath.Join(binding.Workspace, "child-effects.txt"))
	if err != nil || string(effects) != "once\n" {
		t.Fatalf("native child effects repeated: %q %v", effects, err)
	}
}
