//go:build codex_e2e

package launch

import (
	"bufio"
	"bytes"
	"context"
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/yusing/mekugi/apps/mekugi/internal/executionobservation"
	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/backends/codex/nativetrace"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/backends/codex/shellhook"
	"github.com/yusing/mekugi/libraries/commandargs"
	"github.com/yusing/mekugi/modules/agentguidance"
	segmenttransport "github.com/yusing/mekugi/modules/commandsegments/transport"
)

func TestCompactReloadNativeCodex(t *testing.T) {
	for _, prompt := range []string{"stock", "inline", "file", "websocket", "terminal", "slow"} {
		for _, automatic := range []bool{false, true} {
			if automatic && (prompt == "terminal" || prompt == "inline" || prompt == "file" || prompt == "slow") {
				continue
			}
			t.Run(fmt.Sprintf("%s/automatic=%v", prompt, automatic), func(t *testing.T) { testCompactReloadNativeCodex(t, automatic, prompt) })
		}
	}
}
func testCompactReloadNativeCodex(t *testing.T, automatic bool, promptKind string) {
	model := strings.TrimSpace(os.Getenv("MEKUGI_E2E_MODEL"))
	if model == "" {
		t.Fatal("MEKUGI_E2E_MODEL must be set for compaction host acceptance")
	}
	slow := promptKind == "slow"
	terminalMode := promptKind == "terminal"
	socket := promptKind == "websocket"
	if socket || terminalMode || slow {
		promptKind = "stock"
	}
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal("installed Codex required:", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	registry, err := buildToolRegistryForTest(t, ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	workspace := t.TempDir()
	if output, err := exec.Command("git", "init", "--quiet", workspace).CombinedOutput(); err != nil {
		t.Fatalf("fixture repository: %v %s", err, output)
	}
	source := filepath.Join(workspace, "selected.txt")
	if err = os.WriteFile(source, []byte("old source\n"), 0600); err != nil {
		t.Fatal(err)
	}
	blocked := filepath.Join(workspace, "blocked.txt")
	if err := os.WriteFile(blocked, []byte("NEVER_ATTACH_BLOCKED\n"), 0600); err != nil {
		t.Fatal(err)
	}
	provider := &compactSelectionProvider{requests: make(chan []byte, 16), automatic: automatic, reader: registry.Frontends["mcat"], prompt: shellhook.DefaultCompactPrompt, slow: slow}
	prompt := filepath.Join(workspace, "compact-prompt.md")
	if err := os.WriteFile(prompt, []byte("Preserve caller file handover instructions."), 0600); err != nil {
		t.Fatal(err)
	}
	var promptArgs []string
	if promptKind != "stock" {
		promptArgs = []string{"-c", fmt.Sprintf("experimental_compact_prompt_file=%q", prompt)}
		provider.prompt = "Preserve caller file handover instructions."
	}
	if promptKind == "inline" {
		provider.prompt = "Preserve caller inline handover instructions."
		promptArgs = append(promptArgs, "-c", fmt.Sprintf("compact_prompt=%q", provider.prompt))
	}
	proxy := newProxyWithSharedTestRegistry(t, registry)
	store, err := openMekugiReplayStore(registry.ReplayDirectory)
	if err != nil {
		t.Fatal(err)
	}
	setTestProxyStore(proxy, store)
	traceRoot := t.TempDir()
	proxy.nativeTrace = nativetrace.New(traceRoot)
	executor := testRequestExecutor(provider, nil, proxy)
	executor.CompactPrompt = func() string { return provider.prompt + agentguidance.CompactInstructions }
	executor.CompactStore = executionobservation.New(executionobservation.Config{Retained: proxy.retained}).StoreCompact
	var endpoint *inferencegateway.WebSocketEndpoint
	if socket {
		upstream := httptest.NewServer(http.HandlerFunc(provider.serve))
		defer upstream.Close()
		client := newTestProviderClient(upstream.URL, upstream.Client())
		executor.Provider, executor.Client = client, client
		endpoint = inferencegateway.NewWebSocketEndpoint(ctx, time.Minute, client, executor.ExecuteWebSocket)
		defer endpoint.Close()
	}
	handler := inferencegateway.ResponsesHandler(ctx, time.Minute, executor.ExecuteHTTP)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if socket {
			for key, values := range codexAuthHeaders() {
				r.Header[key] = values
			}
		}
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			if endpoint != nil {
				endpoint.ServeHTTP(w, r)
				return
			}
			http.Error(w, "fixture requires HTTP", http.StatusUpgradeRequired)
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" {
			http.NotFound(w, r)
			return
		}
		handler(w, r)
	}))
	defer server.Close()
	environment := builtInOpenAICodexEnvironment(t)
	if slow {
		commands := filepath.Join(workspace, "commands")
		if err := os.Mkdir(commands, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(commands, "skills-mgr"), []byte("#!/bin/bash\nsleep 32\nprintf 'COMPLETE_DELAYED_SKILL\\n'\n"), 0700); err != nil {
			t.Fatal(err)
		}
		for i, entry := range environment {
			if path, ok := strings.CutPrefix(entry, "PATH="); ok {
				environment[i] = "PATH=" + commands + string(os.PathListSeparator) + path
			}
		}
	}
	environment, err = frontendShellEnvironment(environment, filepath.Dir(registry.Frontends["mcat"]), "", nil, segmenttransport.Endpoints{})
	if err != nil {
		t.Fatal(err)
	}
	environment = append(environment, routerTestWorkerEnvironment+"=1", "CODEX_ROLLOUT_TRACE_ROOT="+traceRoot)
	newCommand := func(hostCtx context.Context) *exec.Cmd {
		t.Helper()
		cmd := exec.CommandContext(hostCtx, codex, codexArgs(server.URL+"/v1", []string{"app-server", "-c", fmt.Sprintf("model=%q", model), "-c", "features.plugins=false", "-c", "model_auto_compact_token_limit=100000"}, true, false, true)...)
		cmd.Args = append(cmd.Args, promptArgs...)
		cmd.Args = append(cmd.Args, "-c", "features.code_mode=true", "-c", "features.code_mode_only=true", "-c", `default_permissions="reload-fixture"`, "-c", fmt.Sprintf(`permissions.reload-fixture.filesystem={"/"="read",%q="write",%q="write",%q="deny"}`, workspace, registry.ReplayDirectory, blocked), "-c", "permissions.reload-fixture.network.enabled=false")
		if !terminalMode {
			cmd.Args = append(cmd.Args, "-c", `default_permissions="server-fixture"`, "-c", fmt.Sprintf(`permissions.server-fixture.filesystem={"/"="read",%q="write",%q="write"}`, workspace, registry.ReplayDirectory))
		}
		cmd.Env, cmd.Dir = environment, workspace
		prior, err := shellhook.DiscoverCompact(ctx, cmd)
		if err != nil {
			t.Fatal(err)
		}
		if prior.CompactPrompt != provider.prompt {
			t.Fatalf("selected prompt = %q, want %q", prior.CompactPrompt, provider.prompt)
		}
		cmd.Args = append(cmd.Args, "-c", fmt.Sprintf("compact_prompt=%q", prior.CompactPrompt+agentguidance.CompactInstructions))
		return cmd
	}
	if terminalMode {
		integration := testAppServerIntegration(proxy)
		terminal := startAppResumeTerminalWithIntegration(t, newCommand, "", integration)
		terminal.await("Ready")
		terminal.send("Start the task.\r")
		terminal.await("Task response.")
		terminal.await("╭─ Completed")
		<-provider.requests
		<-provider.requests
		if err := os.WriteFile(source, []byte("fresh selected source\n"), 0600); err != nil {
			t.Fatal(err)
		}
		terminal.send("/compact\r")
		terminal.await("Context compacted")
		terminal.send("Continue after compaction.\r")
		terminal.await("compaction continuation accepted")
		found := false
		for range 2 {
			select {
			case body := <-provider.requests:
				if bytes.Contains(body, []byte("Goal: finish the parser fix.")) {
					found = true
					// This terminal harness explicitly starts full-access threads; API cases below test narrower thread/turn profiles.
					if !bytes.Contains(body, []byte("fresh selected source")) || bytes.Contains(body, []byte("<mekugi-reload-v1>")) {
						t.Fatal("terminal continuation lost fresh sources or leaked selection metadata")
					}
				}
			case <-terminal.ctx.Done():
				t.Fatal("missing terminal compaction or continuation")
			}
		}
		if !found {
			t.Fatal("terminal continuation lost summary")
		}
		terminal.stopCanceled()
		return
	}
	newClient := func() *appserver.Client {
		c, err := appserver.Start(newCommand(ctx))
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	client := newClient()
	defer func() { client.Close() }()
	completed := 0
	next := func() appserver.Message {
		t.Helper()
		for {
			select {
			case m, ok := <-client.Messages:
				if !ok {
					t.Fatal("host closed")
				}
				if m.Method == "error" {
					var failure struct {
						WillRetry bool `json:"willRetry"`
					}
					if json.Unmarshal(m.Params, &failure) != nil || !failure.WillRetry {
						t.Fatalf("host error: %s", m.Params)
					}
				}
				if m.Method == "turn/completed" {
					completed++
				}

				return m
			case <-ctx.Done():
				t.Fatalf("host timeout: %v diagnostics=%s", ctx.Err(), client.Diagnostics.Snapshot())
			}
		}
	}

	reply := func(id string, err error) appserver.Message {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		for {
			m := next()
			if string(m.ID) == id {
				if m.Error != nil {
					t.Fatal(m.Error)
				}
				return m
			}
		}
	}
	call := func(method string, params any) appserver.Message {
		t.Helper()
		return reply(client.Send(method, params, true))
	}
	initialize := func() {
		t.Helper()
		reply(client.Initialize())
		if _, err := client.Send("initialized", nil, false); err != nil {
			t.Fatal(err)
		}
	}
	waitTurns := func(n int) {
		t.Helper()
		for completed < n {
			next()
		}
	}
	runTurn := func(method string, params any) {
		t.Helper()
		target := completed + 1
		call(method, params)
		waitTurns(target)
	}
	request := func() []byte {
		t.Helper()
		select {
		case body := <-provider.requests:
			return body
		case <-ctx.Done():
			t.Fatal("no provider request")
		}
		return nil
	}
	initialize()
	start := map[string]any{"cwd": workspace, "model": model, "approvalPolicy": "never"}
	if !automatic {
		start["permissions"] = "reload-fixture"
	}
	started := call("thread/start", start)
	var result struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err = json.Unmarshal(started.Result, &result); err != nil {
		t.Fatal(err)
	}
	thread := result.Thread.ID
	turn := map[string]any{"threadId": thread, "input": appserver.Input("Start the task.")}
	if automatic {
		turn["permissions"] = "reload-fixture"
	}
	runTurn("turn/start", turn)
	initial := request()
	if strings.Contains(string(initial), "old source") {
		t.Fatal("ordinary startup reloaded selection")
	}
	readResult := request()
	if !bytes.Contains(readResult, []byte("old source")) || !bytes.Contains(readResult, []byte("EACCES")) || bytes.Contains(readResult, []byte("NEVER_ATTACH_BLOCKED")) {
		t.Fatalf("native pre-compaction allow/deny evidence missing: %s", readResult)
	}
	largeSource := "fresh selected source\n" + strings.Repeat("Untruncated full source row for compaction delivery.\n", 3000) + "MIDDLE_SENTINEL_FULL_RELOAD\n" + strings.Repeat("Untruncated full source row for compaction delivery.\n", 3000) + "END_SENTINEL_FULL_RELOAD\n"
	if err = os.WriteFile(source, []byte(largeSource), 0600); err != nil {
		t.Fatal(err)
	}
	if !automatic {
		runTurn("thread/compact/start", map[string]any{"threadId": thread})
		request()
	}
	runTurn("turn/start", map[string]any{"threadId": thread, "input": []any{}})
	if automatic {
		request()
	}
	continued := request()
	wantSource := "fresh selected source"
	wantLarge := true
	check := func(body []byte) {
		t.Helper()
		var forwarded struct {
			Input []struct {
				Type    string `json:"type"`
				Name    string `json:"name"`
				Input   string `json:"input"`
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"input"`
		}
		if err := json.Unmarshal(body, &forwarded); err != nil {
			t.Fatal(err)
		}
		if len(forwarded.Input) == 0 {
			t.Fatal("provider input missing")
		}
		fullFile := false
		for _, item := range forwarded.Input {
			for _, part := range item.Content {
				if strings.Contains(part.Text, largeSource) {
					fullFile = true
				}
			}
			if item.Type == "custom_tool_call" && strings.Contains(item.Input, "mreload") || item.Type == "function_call" && item.Name == "wait" {
				t.Fatal("synthetic reload call reached provider input")
			}
		}
		if wantLarge && !fullFile {
			t.Fatal("complete file must reach the provider in one message")
		}
		if slow && bytes.Count(body, []byte("COMPLETE_DELAYED_SKILL")) != 1 {
			t.Fatal("delayed acquisition did not complete exactly once before inference")
		}
		inputOnly, _ := json.Marshal(forwarded.Input)
		if bytes.Contains(inputOnly, []byte("Script running with cell ID")) || bytes.Contains(inputOnly, []byte("Reload snapshot: mread")) {
			t.Fatal("internal acquisition leaked into provider input")
		}
		if bytes.Count(body, []byte("already read context")) != 1 {
			t.Fatal("reload guidance must appear once")
		}
		if bytes.Contains(body, []byte("Keep the provider's textual context.")) {
			t.Fatalf("non-summary provider output leaked: %s", body)
		}
		if !bytes.Contains(body, []byte("Goal: finish the parser fix. Completed: reproduced the failure. Next: add the regression test.")) || bytes.Contains(body, []byte("<mekugi-reload-v1>")) || bytes.Contains(body, []byte(`"sources"`)) {
			t.Fatalf("continuation lost the substantive handover or leaked metadata: %s", body)
		}
		if !bytes.Contains(body, []byte("Permission denied")) || bytes.Contains(body, []byte("NEVER_ATTACH_BLOCKED")) || !bytes.Contains(body, []byte("created source body")) {
			t.Fatalf("reader permissions or newly created source missing: %s", body)
		}
		if !bytes.Contains(body, []byte(wantSource)) || bytes.Contains(body, []byte("must not reread on resume")) {
			t.Fatalf("fresh native reload missing: %s", body)
		}

	}
	check(continued)
	if err = os.WriteFile(source, []byte("must not reread on resume\n"), 0600); err != nil {
		t.Fatal(err)
	}
	client.Close()
	client = newClient()
	initialize()
	call("thread/resume", map[string]any{"threadId": thread, "cwd": workspace})
	runTurn("turn/start", map[string]any{"threadId": thread, "input": appserver.Input("Continue after resume.")})
	check(request())
	forked := call("thread/fork", map[string]any{"threadId": thread, "cwd": workspace})
	if err := json.Unmarshal(forked.Result, &result); err != nil {
		t.Fatal(err)
	}
	runTurn("turn/start", map[string]any{"threadId": result.Thread.ID, "input": appserver.Input("Continue in the fork.")})
	check(request())
	if promptKind == "stock" && !automatic && !socket && !slow {
		wantSource = "second compaction source"
		wantLarge = false
		if err := os.WriteFile(source, []byte(wantSource), 0600); err != nil {
			t.Fatal(err)
		}
		runTurn("thread/compact/start", map[string]any{"threadId": result.Thread.ID})
		request()
		runTurn("turn/start", map[string]any{"threadId": result.Thread.ID, "input": []any{}})
		check(request())
	}
	if socket && provider.socketCompactions.Load() != 1 {
		t.Fatalf("upstream WebSocket compactions=%d", provider.socketCompactions.Load())
	}
	t.Logf("installed built-in OpenAI compaction: selected-file acquisition, user-role projection, immutable fresh-host resume (%s)", fmt.Sprint(thread))
}

// The provider emits one ordinary summary with separate source-selection metadata.
type compactSelectionProvider struct {
	requests          chan []byte
	automatic         bool
	slow              bool
	count             atomic.Int32
	socketCompactions atomic.Int32
	reader            string
	prompt            string
}

func (p *compactSelectionProvider) serve(w http.ResponseWriter, r *http.Request) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		serveHTTPTestProvider(w, r, p)
		return
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	for {
		fields, err := providerSocketRead(r.Context(), conn)
		if err != nil {
			return
		}
		if string(fields["generate"]) == "false" {
			if providerSocketWrite(r.Context(), conn, socketEvent("response.created", "warm")) != nil || providerSocketWrite(r.Context(), conn, socketEvent("response.completed", "warm")) != nil {
				return
			}
			continue
		}
		var metadata map[string]string
		if err := json.Unmarshal(fields["client_metadata"], &metadata); err != nil {
			return
		}
		headers := r.Header.Clone()
		headers.Set(codexresponses.TurnMetadataHeader, metadata[codexresponses.TurnMetadataHeader])
		if bytes.Contains(fields["input"], []byte("At the end of your handover emit exactly one")) {
			p.socketCompactions.Add(1)
		}
		body, err := json.Marshal(fields)
		if err != nil {
			return
		}
		response, err := p.ForwardExecution(r.Context(), r.Context(), body, headers, "")
		if err != nil {
			return
		}
		scanner := bufio.NewScanner(response.Body)
		for scanner.Scan() {
			if payload, ok := bytes.CutPrefix(scanner.Bytes(), []byte("data: ")); ok {
				if err := conn.Write(r.Context(), websocket.MessageText, payload); err != nil {
					response.Body.Close()
					return
				}
			}
		}
		response.Body.Close()
		if scanner.Err() != nil {
			return
		}
	}
}

func (p *compactSelectionProvider) ForwardExecution(ctx, _ context.Context, body []byte, headers http.Header, _ string) (*inferencegateway.Response, error) {
	p.requests <- bytes.Clone(body)
	count := p.count.Add(1)
	metadata, _ := codexresponses.DecodeTurnMetadata(headers)
	text := "Task response."
	if count >= 4 {
		text = "compaction continuation accepted"
	}
	compaction := bytes.Contains(body, []byte("At the end of your handover emit exactly one"))
	if compaction {
		if metadata.RequestKind != "turn" || len(metadata.Compaction) != 0 && string(metadata.Compaction) != "null" {
			return nil, fmt.Errorf("compaction was not an ordinary provider turn")
		}
		if bytes.Contains(body, []byte(`"compaction_trigger"`)) {
			return nil, fmt.Errorf("opaque compaction trigger reached plaintext provider")
		}
		encodedPrompt, _ := json.Marshal(strings.TrimSpace(p.prompt + agentguidance.CompactInstructions))
		if !bytes.Contains(body, encodedPrompt) || bytes.Count(body, []byte("At the end of your handover emit exactly one")) != 1 {
			return nil, fmt.Errorf("host-selected compaction prompt or unique reload supplement missing")
		}
		// Exercise more than sixteen selected sources.
		selection := `{"sources":[{"path":"selected.txt"},{"path":"blocked.txt"},{"path":"created.txt"}` + strings.Repeat(`,{"path":"selected.txt","ranges":[[1,1]]}`, 17) + `]}`
		if p.slow {
			selection = strings.TrimSuffix(selection, "]}") + `,{"skill":"slow-fixture"}]}`
		}
		text = "Goal: finish the parser fix. Completed: reproduced the failure. Next: add the regression test.\n" + agentguidance.CompactMetadataStart + selection + agentguidance.CompactMetadataEnd
	}
	item := map[string]any{"type": "message", "id": "msg-compact-fixture", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}}}}
	if count == 1 {
		program := fmt.Sprintf("text(await tools.apply_patch(%q)); text(await tools.exec_command({cmd:%q,shell:'bash',login:false})); text(await tools.exec_command({cmd:%q,shell:'bash',login:false}));", "*** Begin Patch\n*** Add File: created.txt\n+created source body\n*** End Patch", commandargs.Quote(p.reader)+" selected.txt", commandargs.Quote(p.reader)+" blocked.txt")
		item = map[string]any{"type": "custom_tool_call", "id": "prior-acquisition", "call_id": "prior-acquisition", "name": "exec", "status": "completed", "input": program}
	}
	output := []any{item}
	if compaction {
		output = append([]any{map[string]any{"type": "reasoning", "id": "rs-compact-fixture", "content": []any{map[string]string{"type": "reasoning_text", "text": "Keep the provider's textual context.\n"}}}}, output...)
	}
	tokens := 10
	if p.automatic && count == 2 {
		tokens = 150000
	}
	wire := routerFaultSSE(map[string]any{"type": "response.created", "response": map[string]any{"id": fmt.Sprint("response-compact-fixture-", count), "status": "in_progress", "output": []any{}}}, map[string]any{"type": "response.output_item.added", "output_index": 0, "item": item}, map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item}, map[string]any{"type": "response.completed", "response": map[string]any{"id": fmt.Sprint("response-compact-fixture-", count), "status": "completed", "output": output, "usage": map[string]any{"input_tokens": tokens, "output_tokens": 1, "total_tokens": tokens + 1}}})
	return testGatewayResponse(serverHTTPResponse(wire), nil)
}
