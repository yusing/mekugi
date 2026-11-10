//go:build codex_e2e

package launch

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/backends/codex/binding"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/backends/codex/shellhook"
	"github.com/yusing/mekugi/libraries/mcptransport"
	segmenttransport "github.com/yusing/mekugi/modules/commandsegments/transport"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func TestCompactReloadNativeCodex(t *testing.T) {
	for _, socket := range []bool{false, true} {
		for _, automatic := range []bool{false, true} {
			t.Run(fmt.Sprintf("websocket=%v/automatic=%v", socket, automatic), func(t *testing.T) { testCompactReloadNativeCodex(t, automatic, socket) })
		}
	}
}

func testCompactReloadNativeCodex(t *testing.T, automatic, socket bool) {
	model := strings.TrimSpace(os.Getenv("MEKUGI_E2E_MODEL"))
	if model == "" {
		t.Fatal("MEKUGI_E2E_MODEL must be set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	registry, err := buildToolRegistryForTest(t, ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	workspace := t.TempDir()
	source := filepath.Join(workspace, "selected.json")
	if err := os.WriteFile(source, []byte("SELECTED_CURRENT_FIELD\nSTALE_UNSELECTED_STATE\n"), 0600); err != nil {
		t.Fatal(err)
	}

	var hintCalls atomic.Int32
	notes := mcp.NewServer(&mcp.Implementation{Name: "notes", Version: "1"}, nil)
	for _, name := range []string{"thread_hint", "read_note", "blocked"} {
		mcp.AddTool(notes, &mcp.Tool{Name: name, Description: "Fixture notes tool", InputSchema: jsontext.Value(`{"type":"object"}`)}, func(_ context.Context, _ *mcp.CallToolRequest, _ map[string]any) (*mcp.CallToolResult, any, error) {
			if name == "thread_hint" {
				hintCalls.Add(1)
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "AUTOMATIC_UNSELECTED_NOTES_SENTINEL"}}}, nil, nil
		})
	}
	notesSocket, stopNotes, err := mcptransport.Start(ctx, notes)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := stopNotes(); err != nil {
			t.Error(err)
		}
	}()
	notesConfig := fmt.Sprintf(`mcp_servers.notes={command=%q,args=["mcp-bridge",%q],enabled=true,disabled_tools=["blocked"],omit_tools_from=["deferred"]}`, os.Args[0], notesSocket)
	provider := &compactSelectionProvider{requests: make(chan []byte, 16), automatic: automatic}
	proxy := newProxyWithSharedTestRegistry(t, registry)
	retained, err := openMekugiReplayStore(registry.ReplayDirectory)
	if err != nil {
		t.Fatal(err)
	}
	setTestProxyStore(proxy, retained)
	executor := testRequestExecutor(provider, nil, proxy)
	var endpoint *inferencegateway.WebSocketEndpoint
	if socket {
		upstream := httptest.NewServer(http.HandlerFunc(provider.serve))
		defer upstream.Close()
		client := newTestProviderClient(upstream.URL, upstream.Client())
		executor.Provider = client
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
			} else {
				http.Error(w, "use HTTP", http.StatusUpgradeRequired)
			}
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" {
			http.NotFound(w, r)
			return
		}
		handler(w, r)
	}))
	defer server.Close()
	environment, err := frontendShellEnvironment(builtInOpenAICodexEnvironment(t), registry.FrontendDirectory, "", nil, segmenttransport.Endpoints{})
	if err != nil {
		t.Fatal(err)
	}
	environment = append(environment, routerTestWorkerEnvironment+"=1")
	hook := filepath.Join(t.TempDir(), "compact-start-hook")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nprintf 'COMPACT_SESSION_START_MARKER\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	hookCommand := quoteShellWord(hook)
	hookHandler := fmt.Sprintf(`{type="command",command=%s,timeout=5,async=false}`, strconv.Quote(hookCommand))
	identity := map[string]any{"event_name": "session_start", "matcher": "^compact$", "hooks": []map[string]any{{"type": "command", "command": hookCommand, "timeout": 5, "async": false}}}
	encoded, err := json.Marshal(identity, json.Deterministic(true))
	if err != nil {
		t.Fatal(err)
	}
	hash := fmt.Sprintf("sha256:%x", sha256.Sum256(encoded))
	hookArgs := []string{"-c", "features.hooks=true", "-c", `hooks.SessionStart=[{matcher="^compact$",hooks=[` + hookHandler + `]}]`, "-c", `hooks.state={"/<session-flags>/config.toml:session_start:0:0"={trusted_hash=` + strconv.Quote(hash) + `,enabled=true}}`}
	newClient := func() *appserver.Client {
		cmd := exec.CommandContext(ctx, "codex", codexArgs(server.URL+"/v1", []string{"app-server", "-c", notesConfig, "--enable=retain_client_tool_outputs", "-c", "features.retain_client_tool_outputs=true", "-c", fmt.Sprintf("model=%q", model), "-c", "features.plugins=false", "-c", "features.multi_agent_v2.enabled=true", "-c", "features.multi_agent_v2.disable_native_tools=true", "-c", "model_auto_compact_token_limit=100000"}, false, true, nil)...)
		cmd.Env, cmd.Dir = environment, workspace
		cmd.Args = append(cmd.Args, hookArgs...)
		_, _, config, err := shellhook.Discover(ctx, cmd, false)
		if err != nil {
			t.Fatal(err)
		}
		override, err := binding.ContextResetConfig(config)
		if err != nil || override == "" {
			t.Fatalf("notes override=%q, %v", override, err)
		}
		cmd.Args = append(cmd.Args, "-c", override)
		client, err := appserver.Start(cmd)
		if err != nil {
			t.Fatal(err)
		}
		return client
	}
	client := newClient()
	defer func() { client.Close() }()
	completed, compactions := 0, 0
	var contextTokens, totalTokens, resetContext, resetTotal uint64
	next := func() appserver.Message {
		for {
			select {
			case m, ok := <-client.Messages:
				if !ok {
					t.Fatal("host closed")
				}
				if m.Method == "error" {
					t.Fatalf("host error: %s", m.Params)
				}
				if m.Method == "thread/tokenUsage/updated" {
					p, err := binding.DecodeNotification(m.Method, m.Params)
					if err != nil {
						t.Fatal(err)
					}
					totalTokens = p.TokenUsage.Total.TotalTokens
					if p.TokenUsage.Last != nil {
						contextTokens = p.TokenUsage.Last.TotalTokens
					}
				}
				if m.Method == "turn/completed" {
					completed++
				}
				if m.Method == "item/completed" && bytes.Contains(m.Params, []byte(`"contextCompaction"`)) {
					compactions++
					resetContext, resetTotal = contextTokens, totalTokens
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
	call := func(method string, params any) appserver.Message { return reply(client.Send(method, params, true)) }
	initialize := func() {
		reply(client.Initialize())
		if _, err := client.Send("initialized", nil, false); err != nil {
			t.Fatal(err)
		}
	}
	runTurn := func(method string, params any) {
		target := completed + 1
		call(method, params)
		for completed < target {
			next()
		}
	}
	request := func() []byte {
		select {
		case body := <-provider.requests:
			return body
		case <-ctx.Done():
			t.Fatal("no provider request")
			return nil
		}
	}
	initialize()
	started := call("thread/start", map[string]any{"cwd": workspace, "model": model, "approvalPolicy": "never", "sandbox": "danger-full-access"})
	var result struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(started.Result, &result); err != nil {
		t.Fatal(err)
	}
	thread := result.Thread.ID
	runTurn("turn/start", map[string]any{"threadId": thread, "input": appserver.Input("ORIGINAL_TASK_SENTINEL")})
	initial, afterTool := request(), request()
	if !bytes.Contains(initial, []byte("read_note")) || bytes.Contains(initial, []byte("mcp__notes__blocked")) {
		t.Fatal("notes override changed explicit tool availability")
	}
	if !bytes.Contains(initial, []byte("ORIGINAL_TASK_SENTINEL")) {
		t.Fatal("fixture lost initial task")
	}
	if automatic {
		if !bytes.Contains(afterTool, []byte("COMPACT_SESSION_START_MARKER")) {
			t.Fatal("automatic reset lost SessionStart hook context before first continuation")
		}
		if bytes.Contains(afterTool, []byte("ORIGINAL_TASK_SENTINEL")) || compactions == 0 {
			t.Fatal("automatic reset did not discard old context")
		}
	} else {
		before := provider.count.Load()
		runTurn("thread/compact/start", map[string]any{"threadId": thread})
		if provider.count.Load() != before {
			t.Fatal("manual reset called the provider")
		}
	}
	if compactions == 0 {
		t.Fatal("missing native compaction lifecycle")
	}
	if resetContext >= 50000 || resetTotal < 50000 {
		t.Fatalf("reset context=%d cumulative=%d", resetContext, resetTotal)
	}
	continuation := map[string]any{"threadId": thread, "input": appserver.Input("Continue normally.")}
	if !automatic {
		wake := control.NativeInput{ID: "fixture:checkpoint", Wake: &control.WaitResult{State: "completed", Compaction: &control.CompactionResult{State: "completed", Reload: `{"sources":["@selected.json#L1-L1","@missing.json#L1-L1"]}`}}}
		continuation["input"] = []any{}
		continuation["toolOutput"] = map[string]any{"namespace": "threadcontrol", "name": "compact", "output": wake.RetainedWaitOutput(), "retain": true}
	}
	runTurn("turn/start", continuation)
	reloaded := request()
	if !automatic && !bytes.Contains(reloaded, []byte("COMPACT_SESSION_START_MARKER")) {
		t.Fatal("SessionStart compact hook context missing from first continuation")
	}
	if bytes.Contains(reloaded, []byte("ORIGINAL_TASK_SENTINEL")) || bytes.Contains(reloaded, []byte("STALE_UNSELECTED_STATE")) {
		t.Fatal("reset retained obsolete context")
	}
	if !automatic && !bytes.Contains(reloaded, []byte("SELECTED_CURRENT_FIELD")) {
		t.Fatalf("selected native reload missing: %.2000s", reloaded)
	}
	for _, body := range [][]byte{initial, afterTool, reloaded} {
		if bytes.Contains(body, []byte(`"name":"new_context"`)) || bytes.Contains(body, []byte("CONTEXT CHECKPOINT COMPACTION")) || bytes.Contains(body, []byte("AUTOMATIC_UNSELECTED_NOTES_SENTINEL")) {
			t.Fatal("direct reset tool or summary prompt exposed")
		}
	}
	var oldFields, newFields map[string]jsontext.Value
	if err := json.Unmarshal(initial, &oldFields); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(reloaded, &newFields); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(oldFields["prompt_cache_key"], newFields["prompt_cache_key"]) || !bytes.Equal(oldFields["tools"], newFields["tools"]) {
		t.Fatal("reset changed cached tool prefix identity")
	}
	original := call("thread/read", map[string]any{"threadId": thread, "includeTurns": true})
	if !bytes.Contains(original.Result, []byte("ORIGINAL_TASK_SENTINEL")) {
		t.Fatal("original task is unavailable for ordinary recall")
	}
	client.Close()
	client = newClient()
	initialize()
	call("thread/resume", map[string]any{"threadId": thread})
	if err := os.WriteFile(source, []byte("CHANGED_AFTER_CAPTURE\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runTurn("turn/start", map[string]any{"threadId": thread, "input": appserver.Input("Continue after restart.")})
	resumed := request()
	if bytes.Contains(resumed, []byte("ORIGINAL_TASK_SENTINEL")) || bytes.Contains(resumed, []byte("CHANGED_AFTER_CAPTURE")) {
		t.Fatal("cold resume resurrected history or reacquired a frozen reload")
	}
	if !automatic && !bytes.Contains(resumed, []byte("SELECTED_CURRENT_FIELD")) {
		t.Fatal("cold resume lost selected reload")
	}
	runTurn("thread/compact/start", map[string]any{"threadId": thread})
	runTurn("turn/start", map[string]any{"threadId": thread, "input": appserver.Input("Continue without a reload.")})
	if fresh := request(); bytes.Contains(fresh, []byte("SELECTED_CURRENT_FIELD")) || bytes.Contains(fresh, []byte("CHANGED_AFTER_CAPTURE")) {
		t.Fatal("later reset reused an obsolete reload")
	}
	if hintCalls.Load() != 0 {
		t.Fatalf("automatic notes calls=%d", hintCalls.Load())
	}
	if provider.count.Load() != 5 {
		t.Fatalf("unexpected provider calls: %d", provider.count.Load())
	}
}

type compactSelectionProvider struct {
	requests  chan []byte
	automatic bool
	count     atomic.Int32
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
	metadata, _ := codexresponses.DecodeTurnMetadata(headers)
	if metadata.RequestKind == "compaction" {
		return nil, fmt.Errorf("summary-free reset made a compaction inference request")
	}
	select {
	case p.requests <- bytes.Clone(body):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	count := p.count.Add(1)
	item := map[string]any{"type": "message", "id": fmt.Sprint("answer-", count), "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "Task response.", "annotations": []any{}}}}
	if count == 1 {
		item = map[string]any{"type": "custom_tool_call", "id": "stock-exec", "call_id": "stock-exec", "name": "exec", "status": "completed", "input": `text("stock JavaScript execution continued");`}
	}
	tokens := 10
	if !p.automatic && count <= 2 {
		tokens = 50000
	}
	if p.automatic && count == 1 {
		tokens = 150000
	}
	id := fmt.Sprint("reset-response-", count)
	wire := routerFaultSSE(map[string]any{"type": "response.created", "response": map[string]any{"id": id, "status": "in_progress", "output": []any{}}}, map[string]any{"type": "response.output_item.added", "output_index": 0, "item": item}, map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item}, map[string]any{"type": "response.completed", "response": map[string]any{"id": id, "status": "completed", "output": []any{item}, "usage": map[string]any{"input_tokens": tokens, "output_tokens": 1, "total_tokens": tokens + 1}}})
	return testGatewayResponse(serverHTTPResponse(wire), nil)
}
