//go:build codex_e2e

package launch

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/modules/conversation/composer"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
)

type nativeThreadsProvider struct {
	mu                                      sync.Mutex
	childDirectory, parent, child, waitTurn string
	fullHistory                             bool
	shellWait                               bool
	preload                                 bool
	calls                                   map[string]int
	users                                   int
	waiting, mailbox                        chan struct{}
	stop                                    context.CancelFunc
}

// Only fixture state and host identities are included. Provider request bodies,
// authorization headers and inherited process environment stay out of failures.
func (p *nativeThreadsProvider) diagnostic() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return fmt.Sprintf("provider inference counts=%v; parent=%q child=%q wait_turn=%q; want initial parent=5 child=2, recovered parent=8 child=3", p.calls, p.parent, p.child, p.waitTurn)
}

func (p *nativeThreadsProvider) serveSocket(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		t.Error("accept local provider WebSocket:", err)
		return
	}
	defer conn.CloseNow()
	for {
		fields, err := providerSocketRead(r.Context(), conn)
		if err != nil {
			return
		}
		if string(fields["generate"]) == "false" {
			if providerSocketWrite(r.Context(), conn, socketEvent("response.created", "managed-prewarm")) != nil ||
				providerSocketWrite(r.Context(), conn, socketEvent("response.completed", "managed-prewarm")) != nil {
				return
			}
			continue
		}
		body, err := json.Marshal(&fields)
		if err != nil {
			t.Error(err)
			return
		}
		request := r.Clone(r.Context())
		request.Body = io.NopCloser(bytes.NewReader(body))
		// A reused socket carries current turn metadata on response.create.
		var metadata map[string]string
		if err := json.Unmarshal(fields["client_metadata"], &metadata); err == nil {
			for key, value := range metadata {
				request.Header.Set(key, value)
			}
		}
		response := httptest.NewRecorder()
		p.serve(t, response, request)
		if response.Code != http.StatusOK {
			_ = providerSocketWrite(r.Context(), conn, map[string]any{"type": "error", "error": map[string]string{"type": "server_error", "message": "managed-thread fixture rejected request"}})
			return
		}
		for line := range strings.SplitSeq(response.Body.String(), "\n") {
			if payload, ok := strings.CutPrefix(line, "data: "); ok {
				if err := conn.Write(r.Context(), websocket.MessageText, []byte(payload)); err != nil {
					return
				}
			}
		}
	}
}

func (p *nativeThreadsProvider) serve(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	var request struct {
		Generate         *bool                       `json:"generate"`
		Instructions     string                      `json:"instructions"`
		Tools            []jsontext.Value            `json:"tools"`
		Input            []map[string]jsontext.Value `json:"input"`
		Model            string                      `json:"model"`
		PreviousResponse string                      `json:"previous_response_id"`
		Reasoning        struct {
			Effort string `json:"effort"`
		} `json:"reasoning"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		t.Error(err)
		http.Error(w, "invalid request", 500)
		return
	}
	if request.Generate != nil && !*request.Generate {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"native-prewarm\",\"status\":\"in_progress\",\"output\":[]}}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"native-prewarm\",\"status\":\"completed\",\"output\":[]}}\n\n")
		return
	}
	metadata, ok := codexresponses.DecodeTurnMetadata(r.Header)
	if !ok && request.Model == "gpt-6-luna" && len(request.Tools) == 0 && strings.HasPrefix(request.Instructions, "Generate a concise session title") && r.Header.Get("Authorization") == "Bearer fixture-access" && r.Header.Get("ChatGPT-Account-Id") == "fixture-account" {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"native-title\",\"status\":\"in_progress\",\"output\":[]}}\n\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"Native thread fixture\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"native-title\",\"status\":\"completed\",\"output\":[]}}\n\n")
		return
	}
	if err != nil || !ok || metadata.ThreadID == "" {
		t.Errorf("upstream host identity/body: %v, valid=%v, generate=%v model=%q", err, ok, request.Generate, request.Model)
		http.Error(w, "missing host identity", 500)
		return
	}
	p.mu.Lock()
	resumedParent := metadata.ThreadID == p.parent && p.calls[p.parent] >= 5
	p.mu.Unlock()
	wantEffort := "low"
	if resumedParent {
		wantEffort = "high"
	}
	if request.Model != "gpt-6-sol" || request.Reasoning.Effort != wantEffort {
		t.Errorf("selected thread settings = %s/%s, want gpt-6-sol/%s", request.Model, request.Reasoning.Effort, wantEffort)
		http.Error(w, "wrong selected settings", 500)
		return
	}
	users := 0
	for _, item := range request.Input {
		if string(item["role"]) == `"user"` {
			users++
		}
	}
	p.mu.Lock()
	if p.parent == "" {
		p.parent = metadata.ThreadID
	}
	p.calls[metadata.ThreadID]++
	step, parent := p.calls[metadata.ThreadID], metadata.ThreadID == p.parent
	if !parent {
		p.child = metadata.ThreadID
	}
	var program, answer, failure string
	if !p.fullHistory && step == 1 {
		rootCount := strings.Count(string(body), "You are `/root`, the primary agent in a team")
		childCount := strings.Count(string(body), "You are an agent in a team of agents collaborating to complete a task.")
		wantRoot, wantChild := 0, 1
		if parent {
			wantRoot, wantChild = 1, 0
		}
		if rootCount != wantRoot || childCount != wantChild || strings.Count(string(body), "<multi_agent_mode>") != 1 {
			failure = fmt.Sprintf("stock guidance counts: root=%d child=%d parent=%t", rootCount, childCount, parent)
		}
	}
	switch {
	case parent && step == 1:
		p.users = users
		program = `text(Object.keys(tools).filter(name => name.includes("threadcontrol"))); text(await tools.mcp__threadcontrol__list_agents({})); text(await tools.mcp__threadcontrol__inspect({target:"/root"}));`
	case parent && step == 2:
		if !strings.Contains(string(body), "compactions") || !strings.Contains(string(body), "context_window") || !strings.Contains(string(body), "reasoning_effort") {
			failure = "root inspection omitted applied settings or metrics"
		}
		if strings.Contains(string(body), "context_known") || strings.Contains(string(body), "compactions_known") {
			failure = "root inspection exposed availability flags"
		}
		if !strings.Contains(string(body), "mcp__threadcontrol__spawn_agent") || !strings.Contains(string(body), "/root") {
			failure = "replacement MCP discovery/list_agents omitted root or spawn tool"
		}
		fork := `,fork_turns:"none"`
		if p.fullHistory {
			fork = "" // Omission exercises the public default, full history.
		}
		if p.preload {
			fork += `,preload:{sources:[{path:"preload.txt",ranges:[[2,2]]},{path:"missing.txt"},{path:"AGENTS.md"}]}`
		}
		program = `text(await tools.mcp__threadcontrol__spawn_agent({task_name:"worker",message:"CHILD_ATTRIBUTED_ASSIGNMENT"` + fork + `,cwd:` + strconv.Quote(p.childDirectory) + `}));`
	case parent && step == 3:
		if !strings.Contains(string(body), "/root/worker") {
			failure = "spawn omitted confirmed logical child"
			for _, item := range request.Input {
				if string(item["type"]) == `"function_call_output"` || string(item["type"]) == `"custom_tool_call_output"` {
					failure += "\n" + string(item["output"])
				}
			}
		}
		program = `text("PARENT_ACCEPTED_MAILBOX");`
	case parent && step == 4:
		if !strings.Contains(string(body), "CHILD_PROGRESS_ALREADY_READ") {
			failure = "parent input omitted injected mailbox message"
		}
		p.waitTurn = metadata.TurnID
		program = `text(await tools.mcp__threadcontrol__wait_agent({target:["worker"]}));`
	case parent && step == 5:
		if p.shellWait {
			if metadata.TurnID == p.waitTurn || users != p.users+1 || strings.Count(string(body), "<user_shell_command>") != 1 || !strings.Contains(string(body), "SHELL_WAIT_OUTPUT") {
				failure = fmt.Sprintf("shell continuation: same_turn=%t user_items=%d want=%d shell_records=%d has_output=%t", metadata.TurnID == p.waitTurn, users, p.users+1, strings.Count(string(body), "<user_shell_command>"), strings.Contains(string(body), "SHELL_WAIT_OUTPUT"))
			}
		} else if metadata.TurnID == p.waitTurn || users != p.users || !strings.Contains(string(body), "MANAGED_CHILD_FINAL") || !strings.Contains(string(body), "/root/worker") || !strings.Contains(string(body), "wait_agent") {
			failure = "local stop inferred again or wake omitted retained attributed result/added human input"
		}
		answer = "MANAGED_PARENT_RESUMED"
	case parent && step == 6:
		program = `text(await tools.mcp__threadcontrol__list_agents({})); text(await tools.mcp__threadcontrol__followup_task({target:"worker",message:"COLD_CHILD_FOLLOWUP"}));`
	case parent && step == 7:
		program = `text(await tools.mcp__threadcontrol__wait_agent({target:["worker"]}));`
	case parent && step == 8:
		if !strings.Contains(string(body), "MANAGED_CHILD_RECOVERED") {
			failure = "cold recovery omitted the retained child's attributed result"
		}
		answer = "MANAGED_PARENT_RECOVERED"
	case !parent && step == 1:
		assignment, environment := false, false
		var developerText strings.Builder
		for _, item := range request.Input {
			var content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if json.Unmarshal(item["content"], &content) != nil {
				continue
			}
			for _, part := range content {
				if string(item["role"]) == `"developer"` {
					developerText.WriteString(part.Text)
				}
				if part.Text == "Message Type: NEW_TASK\nTask name: /root/worker\nSender: /root\nPayload:\nCHILD_ATTRIBUTED_ASSIGNMENT" && string(item["role"]) == `"assistant"` && string(item["phase"]) == `"commentary"` && part.Type == "output_text" {
					assignment = true
				}
				if string(item["role"]) == `"user"` && part.Type == "input_text" && strings.HasPrefix(part.Text, "<environment_context>") && strings.Contains(part.Text, "<cwd>"+p.childDirectory+"</cwd>") && !strings.Contains(part.Text, "CHILD_ATTRIBUTED_ASSIGNMENT") {
					environment = true
				}
			}
		}
		wantUsers := 1
		if p.preload {
			wantUsers += 2 // One shared reload context message per selected reader outcome.
		}
		inherited := strings.Contains(string(body), "PARENT_PRIVATE_SENTINEL")
		if inherited != p.fullHistory || !assignment || !environment || !p.fullHistory && users != wantUsers {
			failure = fmt.Sprintf("child attribution: full_history=%v parent_input=%v assistant_assignment=%v environment=%v user_items=%d", p.fullHistory, inherited, assignment, environment, users)
		}
		if p.preload && (!strings.Contains(string(body), "CHILD_PRELOAD_CONTENT") || !strings.Contains(string(body), "missing.txt") || !strings.Contains(string(body), "exit_code=1") || strings.Contains(string(body), "UNSELECTED_PRELOAD_CONTENT") || strings.Contains(string(body), "PARENT_PRELOAD_CONTENT")) {
			failure = "initial child inference did not receive selected child-cwd preload and explicit read failure"
		}
		if p.preload {
			if err := os.WriteFile(filepath.Join(p.childDirectory, "preload.txt"), []byte("UNSELECTED_PRELOAD_CONTENT\nCHANGED_PRELOAD_CONTENT\n"), 0600); err != nil {
				failure = err.Error()
			}
		}
		if p.fullHistory {
			guidance := developerText.String()
			if strings.Count(guidance, "NATIVE_FIXTURE_CHILD_ROLE") != 1 || strings.Count(guidance, "Your logical task is /root/worker.") != 1 || strings.Contains(guidance, "NATIVE_FIXTURE_ROOT_ROLE") {
				failure = "full-history child guidance omitted child role or retained duplicate/parent generated role"
			}
		}
		program = `text(await tools.mcp__threadcontrol__send_message({target:"/root",message:"CHILD_PROGRESS_ALREADY_READ"})); text(await tools.exec_command({cmd:"pwd; printf managed-child-write > managed-child.txt",shell:"/bin/bash",login:false}));`
	case !parent && step == 2:
		receipt := false
		for _, item := range request.Input {
			if string(item["type"]) == `"function_call_output"` || string(item["type"]) == `"custom_tool_call_output"` {
				receipt = receipt || strings.Contains(string(item["output"]), "mchanges ")
			}
		}
		if !receipt {
			failure = "child shell tool output omitted its retained capture receipt"
		}
		if !strings.Contains(string(body), p.childDirectory) {
			failure = "stock shell did not return selected cwd"
		}
		if p.preload && ((!strings.Contains(string(body), "CHILD_PRELOAD_CONTENT") && request.PreviousResponse != "managed-"+metadata.ThreadID+"-1") || strings.Contains(string(body), "CHANGED_PRELOAD_CONTENT")) {
			failure = "child continuation reread or lost its completed preload"
		}
		answer = "MANAGED_CHILD_FINAL"
	case !parent && step == 3:
		if !strings.Contains(string(body), "COLD_CHILD_FOLLOWUP") || !strings.Contains(string(body), "CHILD_ATTRIBUTED_ASSIGNMENT") {
			failure = "cold recovery omitted retained history or the explicit follow-up"
		}
		answer = "MANAGED_CHILD_RECOVERED"
	default:
		failure = fmt.Sprintf("unexpected inference: parent=%v step=%d", parent, step)
	}
	p.mu.Unlock()
	if failure != "" {
		t.Error(failure)
		http.Error(w, failure, 500)
		p.stop()
		return
	}
	if parent && step == 3 {
		select {
		case <-p.mailbox:
		case <-r.Context().Done():
			return
		}
	}
	if !parent && step == 2 {
		close(p.mailbox)
		select {
		case <-p.waiting:
		case <-r.Context().Done():
			return
		}
	}
	id := fmt.Sprintf("managed-%s-%d", metadata.ThreadID, step)
	item := map[string]any{"type": "message", "id": id, "role": "assistant", "phase": "final_answer", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": answer}}}
	if program != "" {
		item = map[string]any{"type": "custom_tool_call", "id": id, "call_id": id + "-call", "name": "exec", "namespace": "functions", "input": program, "status": "completed"}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	for _, event := range []map[string]any{
		{"type": "response.created", "response": map[string]any{"id": id, "status": "in_progress", "output": []any{}}},
		{"type": "response.output_item.added", "output_index": 0, "item": item},
		{"type": "response.output_item.done", "output_index": 0, "item": item},
		{"type": "response.completed", "response": map[string]any{"id": id, "status": "completed", "output": []any{item}}},
	} {
		encoded, err := json.Marshal(event)
		if err != nil {
			t.Error(err)
			return
		}
		fmt.Fprintf(w, "data: %s\n\n", encoded)
	}
}

// The public server owns both installed host processes; this client uses native
// semantic controls without a terminal renderer or live provider credentials.
func TestNativeThreadsServerOwnedCodex(t *testing.T) {
	testNativeThreadsServerOwnedCodex(t, nativeThreadsScenario{})
}

func TestNativeThreadsFullHistoryServerOwnedCodex(t *testing.T) {
	testNativeThreadsServerOwnedCodex(t, nativeThreadsScenario{fullHistory: true})
}

func TestNativeThreadsShellWaitServerOwnedCodex(t *testing.T) {
	testNativeThreadsServerOwnedCodex(t, nativeThreadsScenario{shellWait: true})
}

func TestNativeThreadsNonGitServerOwnedCodex(t *testing.T) {
	testNativeThreadsServerOwnedCodex(t, nativeThreadsScenario{nonGit: true})
}

func TestNativeThreadsPreloadServerOwnedCodex(t *testing.T) {
	testNativeThreadsServerOwnedCodex(t, nativeThreadsScenario{preload: true})
}

type nativeThreadsScenario struct {
	fullHistory, shellWait, nonGit, preload bool
}

func testNativeThreadsServerOwnedCodex(t *testing.T, scenario nativeThreadsScenario) {
	t.Helper()
	fullHistory, shellWait := scenario.fullHistory, scenario.shellWait
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal("installed Codex is required:", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	environment := builtInOpenAICodexEnvironment(t)
	var configRoot string
	for _, entry := range environment {
		if value, ok := strings.CutPrefix(entry, "CODEX_HOME="); ok {
			configRoot = value
		}
	}
	claims := `{"email":"fixture@example.test","https://api.openai.com/auth":{"chatgpt_account_id":"fixture-account","chatgpt_plan_type":"pro"}}`
	token := "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + ".c2ln"
	auth, err := json.Marshal(map[string]any{
		"tokens":       map[string]string{"id_token": token, "access_token": "fixture-access", "refresh_token": "fixture-refresh", "account_id": "fixture-account"},
		"last_refresh": time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configRoot, "auth.json"), auth, 0600); err != nil {
		t.Fatal(err)
	}
	bundled := exec.CommandContext(ctx, codex, "debug", "models", "--bundled")
	bundled.Env = environment
	catalog, err := bundled.Output()
	if err != nil {
		t.Fatal("bundled account catalog:", err)
	}
	var modelCatalog map[string]any
	if err := json.Unmarshal(catalog, &modelCatalog); err != nil {
		t.Fatal(err)
	}
	for _, entry := range modelCatalog["models"].([]any) {
		model := entry.(map[string]any)
		if model["slug"] != "gpt-6-sol" {
			continue
		}
		fields := model
		for _, key := range []string{"model_messages", "multi_agent", "role"} {
			child, ok := fields[key].(map[string]any)
			if !ok {
				child = make(map[string]any)
				fields[key] = child
			}
			fields = child
		}
		if fullHistory {
			fields["root"], fields["subagent"] = "NATIVE_FIXTURE_ROOT_ROLE", "NATIVE_FIXTURE_CHILD_ROLE"
		} else {
			delete(fields, "root")
			delete(fields, "subagent")
		}
	}
	catalog, err = json.Marshal(modelCatalog)
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	if !scenario.nonGit {
		workspace = gitTestWorkspace(t)
	}
	childDirectory := workspace
	if fullHistory {
		repository := newGitFixture(t)
		repository.run("commit", "--allow-empty", "-q", "-m", "base")
		childDirectory = filepath.Join(t.TempDir(), "child-worktree")
		repository.run("worktree", "add", "-q", "--detach", childDirectory, "HEAD")
	}
	if scenario.preload {
		childDirectory = t.TempDir()
		for path, body := range map[string]string{
			filepath.Join(workspace, "preload.txt"):      "PARENT_PRELOAD_CONTENT\n",
			filepath.Join(childDirectory, "preload.txt"): "UNSELECTED_PRELOAD_CONTENT\nCHILD_PRELOAD_CONTENT\n",
		} {
			if err := os.WriteFile(path, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	fixture := &nativeThreadsProvider{preload: scenario.preload, childDirectory: childDirectory, fullHistory: fullHistory, shellWait: shellWait, calls: make(map[string]int), waiting: make(chan struct{}), mailbox: make(chan struct{}), stop: cancel}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/models"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(catalog)
		case strings.HasSuffix(r.URL.Path, "/responses") && strings.EqualFold(r.Header.Get("Upgrade"), "websocket"):
			fixture.serveSocket(t, w, r)
		case strings.HasSuffix(r.URL.Path, "/responses") && r.Method == http.MethodPost:
			fixture.serve(t, w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	socketDirectory, err := os.MkdirTemp("", "native-threads-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(socketDirectory)
	socket := filepath.Join(socketDirectory, "s")
	environment = append(environment, "XDG_STATE_HOME="+t.TempDir(),
		"MEKUGI_NATIVE_THREADS_SOCKET="+socket,
		"MEKUGI_NATIVE_THREADS_ADDRESS="+server.Listener.Addr().String(),
		"MEKUGI_NATIVE_THREADS_CERT="+base64.StdEncoding.EncodeToString(server.Certificate().Raw),
		routerTestWorkerEnvironment+"=1")
	cmd := exec.Command(os.Args[0], "-test.run=^TestNativeThreadsServerProcessHelper$")
	cmd.Env, cmd.Dir = environment, workspace
	var diagnostics bytes.Buffer
	cmd.Stdout, cmd.Stderr = &diagnostics, &diagnostics
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	joined := make(chan struct{})
	var processErr error
	go func() { processErr = cmd.Wait(); close(joined) }()
	defer func() {
		_ = cmd.Process.Signal(os.Interrupt)
		select {
		case <-joined:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
			<-joined
			t.Error("server did not join its native runtimes")
		}
		if processErr != nil || t.Failed() {
			t.Logf("server outcome: %v\n%s\n%s", processErr, &diagnostics, fixture.diagnostic())
		}
	}()
	serverCLIAwait(t, func() bool {
		select {
		case <-joined:
			t.Fatalf("server stopped before readiness: %v\n%s", processErr, &diagnostics)
		default:
		}
		_, err := os.Stat(socket)
		return err == nil
	})
	transport := sessionapi.ControlTransport(socket)
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	connection, err := sessionapi.StartNative(ctx, client, "http://mekugi", sessionapi.NativeStart{
		Workspace: workspace, RouterArgs: []string{"--shell-guard=false"},
		Args: []string{"--yolo", "-m", "gpt-6-sol", "-c", "features.plugins=false", "-c", `model_reasoning_effort="low"`},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	var event sessionapi.NativeEvent
	for event.Frame.View.Main.ID == "" {
		event, err = connection.Next()
		if err != nil {
			t.Fatal("root native readiness:", err)
		}
	}
	waitingObserved := false
	root := event.Frame.View.Main.ID
	_, err = connection.Send(ctx, event.Target(), sessionapi.NativeOperation{Composer: &sessionapi.NativeComposerInput{
		Intent: sessionpresentation.ComposerIntent{Version: event.Frame.View.Version, Thread: root, Operation: sessionpresentation.SubmitComposer},
		Draft:  composer.Draft{Text: "PARENT_PRIVATE_SENTINEL delegate fresh work"},
	}})
	if err != nil {
		t.Fatal("root input:", err)
	}
	for {
		event, err = connection.Next()
		if err != nil {
			t.Fatal("parent completion:", err)
		}
		if event.Frame.View.Status.Composer.Text == "Waiting for agents" {
			if !event.Frame.Facts.Busy {
				t.Fatal("logical waiting lost the native terminal-title busy state")
			}
			if !waitingObserved {
				waitingObserved = true
				if shellWait {
					_, err = connection.Send(ctx, event.Target(), sessionapi.NativeOperation{Composer: &sessionapi.NativeComposerInput{
						Intent: sessionpresentation.ComposerIntent{Version: event.Frame.View.Version, Thread: root, Operation: sessionpresentation.SubmitComposer},
						Draft:  composer.Draft{Text: "!printf SHELL_WAIT_OUTPUT"},
					}})
					if err != nil {
						t.Fatal("shell during logical waiting:", err)
					}
				} else {
					close(fixture.waiting)
				}
			}
		}
		body, err := json.Marshal(event.Frame, jsonv1.FormatDurationAsNano(true))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "MANAGED_PARENT_RESUMED") {
			break
		}
		if shellWait && waitingObserved && event.Frame.View.Status.Session.CompletionKnown {
			t.Fatal("standalone shell marked Main done before model continuation")
		}
	}
	if shellWait {
		fixture.mu.Lock()
		calls := fixture.calls[root]
		fixture.mu.Unlock()
		if calls != 5 {
			t.Fatalf("shell resumed Main more than once: %d inference calls", calls)
		}
		close(fixture.waiting)
		return
	}
	childReply, childMessage := false, false
	if event.Frame.Main != nil {
		for _, entry := range event.Frame.Main.Entries {
			childReply = childReply || entry.Agent == "/root/worker" && entry.Kind == "final" && entry.Text == "MANAGED_CHILD_FINAL"
			childMessage = childMessage || entry.Agent == "/root/worker" && entry.Kind == "reply" && entry.Text == "CHILD_PROGRESS_ALREADY_READ"
		}
	}
	if !childReply || !childMessage {
		t.Fatalf("delivered delegate traffic missing from Main transcript: final=%t message=%t", childReply, childMessage)
	}
	fixture.mu.Lock()
	parent, child := fixture.parent, fixture.child
	counts := make(map[string]int, len(fixture.calls))
	for id, n := range fixture.calls {
		counts[id] = n
	}
	fixture.mu.Unlock()
	if parent != root || child == "" || child == parent || len(counts) != 2 || counts[parent] != 5 || counts[child] != 2 {
		t.Fatalf("native host identities/inferences: root=%q parent=%q child=%q calls=%v", root, parent, child, counts)
	}
	childView, err := connection.Attach(ctx, child)
	if err != nil {
		t.Fatal("independent child runtime attachment:", err)
	}
	defer childView.Close()
	childEvent, err := childView.Next()
	if err != nil || childEvent.Frame.View.Main.ID != child || childEvent.Attachment == event.Attachment {
		t.Fatalf("independent child runtime publication: child=%q attachment=%q error=%v", childEvent.Frame.View.Main.ID, childEvent.Attachment, err)
	}
	if scenario.preload {
		attached, failed, internal := false, false, false
		var operations []string
		if childEvent.Frame.Main != nil {
			for _, entry := range childEvent.Frame.Main.Entries {
				for _, value := range entry.Values {
					if value.Kind == "op" {
						operations = append(operations, fmt.Sprintf("%s path=%q reload=%t exited=%t exit=%d code=%.180q", value.Verb, value.Path, value.AutoReload, value.Exited, value.ExitCode, value.Code))
					}
					attached = attached || value.Verb == "Attached" && value.Path == "preload.txt" && value.Exited && value.ExitCode == 0
					failed = failed || value.Verb == "Attached" && value.Path == "missing.txt" && value.Exited && value.ExitCode == 1
					internal = internal || value.Verb == "Run" && strings.Contains(value.Code, "mreload") || value.Verb == "Attached" && (len(value.Tail) > 0 || value.AutoReload)
				}
			}
		}
		if !attached || !failed || internal {
			t.Fatalf("preload attachment presentation: attached=%t failed=%t internal=%t operations=%v", attached, failed, internal, operations)
		}
	}
	if fullHistory {
		if _, err := os.Stat(filepath.Join(workspace, "managed-child.txt")); !os.IsNotExist(err) {
			t.Fatalf("child shell wrote in parent workspace: %v", err)
		}
	}
	data, err := os.ReadFile(filepath.Join(childDirectory, "managed-child.txt"))
	if err != nil || string(data) != "managed-child-write" {
		t.Fatalf("inherited child cwd effect: %q, %v", data, err)
	}
	for {
		review := childEvent.Frame.Review
		if review == nil || review.Workspace != childDirectory {
			t.Fatalf("child saved review lost its admitted workspace: %+v", review)
		}
		found := false
		for _, file := range review.Files {
			for _, chunk := range file.Chunks {
				found = found || chunk.Review.AfterPath == filepath.Join(childDirectory, "managed-child.txt")
			}
		}
		if found {
			break
		}
		// Review hydration publishes independently of task completion.
		childEvent, err = childView.Next()
		if err != nil {
			t.Fatalf("child workspace edit missing from saved Diff: %v; review=%+v", err, review)
		}
	}

	if scenario.preload {
		return // Acquisition and prompt projection acceptance finish before cold recovery.
	}

	// A new server and host process recover only retained identities. Attaching a
	// resumed parent leaves its child cold until explicit selection or input.
	childView.Close()
	connection.Close()
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	select {
	case <-joined:
	case <-ctx.Done():
		t.Fatal("cold shutdown:", ctx.Err())
	}
	if processErr != nil {
		t.Fatal("cold shutdown:", processErr)
	}
	cmd = exec.Command(os.Args[0], "-test.run=^TestNativeThreadsServerProcessHelper$")
	cmd.Env, cmd.Dir, cmd.Stdout, cmd.Stderr = environment, workspace, &diagnostics, &diagnostics
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	joined = make(chan struct{})
	go func() { processErr = cmd.Wait(); close(joined) }()
	serverCLIAwait(t, func() bool { _, err := os.Stat(socket); return err == nil })
	connection, err = sessionapi.StartNative(ctx, client, "http://mekugi", sessionapi.NativeStart{
		Workspace: workspace, RouterArgs: []string{"--shell-guard=false"},
		Args: []string{"--yolo", "-c", "features.plugins=false", "-c", `model_reasoning_effort="high"`, "resume", root},
	})
	if err != nil {
		t.Fatal("cold root resume:", err)
	}
	defer connection.Close()
	var readiness string
	for {
		event, err = connection.Next()
		if err != nil {
			t.Fatal("cold root readiness:", err, readiness)
		}
		retainedChild := false
		if family := event.Frame.Family; family != nil && family.Agents != nil {
			for _, row := range family.Agents.Roster {
				if row.Agent.Thread == child && row.Agent.Name == "/root/worker" && row.Agent.Final {
					retainedChild = true
				}
			}
		}
		if event.Frame.View.Main.ID == root && event.Frame.View.Admission.Submit && retainedChild {
			break
		}
		readiness = fmt.Sprintf("thread=%q submit=%t retained_child=%t", event.Frame.View.Main.ID, event.Frame.View.Admission.Submit, retainedChild)
	}
	childReply, childMessage = false, false
	if event.Frame.Main != nil {
		for _, entry := range event.Frame.Main.Entries {
			childReply = childReply || entry.Agent == "/root/worker" && entry.Kind == "final" && entry.Text == "MANAGED_CHILD_FINAL"
			childMessage = childMessage || entry.Agent == "/root/worker" && entry.Kind == "reply" && entry.Text == "CHILD_PROGRESS_ALREADY_READ"
		}
	}
	if !childReply || !childMessage {
		t.Fatalf("cold Main lost delivered delegate traffic: final=%t message=%t", childReply, childMessage)
	}
	attachedChild, err := connection.Attach(ctx, child)
	if err != nil {
		t.Fatal("explicit cold child selection:", err)
	}
	coldChild, err := attachedChild.Next()
	if err != nil {
		t.Fatal("cold child publication:", err)
	}
	if coldChild.Frame.View.Main.ID != child || coldChild.Frame.View.Caption.Model != "gpt-6-sol" || coldChild.Frame.View.Caption.Effort != "low" || !fullHistory && coldChild.Frame.View.Caption.Role == "" {
		t.Fatalf("cold child identity=%q settings=%+v", coldChild.Frame.View.Main.ID, coldChild.Frame.View.Caption)
	}
	fixture.mu.Lock()
	if fixture.calls[parent] != counts[parent] || fixture.calls[child] != counts[child] {
		t.Errorf("child selection started inference: %v", fixture.calls)
	}
	fixture.mu.Unlock()
	attachedChild.Close()
	_, err = connection.Send(ctx, event.Target(), sessionapi.NativeOperation{Composer: &sessionapi.NativeComposerInput{
		Intent: sessionpresentation.ComposerIntent{Version: event.Frame.View.Version, Thread: root, Operation: sessionpresentation.SubmitComposer},
		Draft:  composer.Draft{Text: "Resume the retained worker with new work."},
	}})
	if err != nil {
		t.Fatal("cold follow-up:", err)
	}
	for {
		event, err = connection.Next()
		if err != nil {
			t.Fatal("cold completion:", err)
		}
		body, err := json.Marshal(event.Frame, jsonv1.FormatDurationAsNano(true))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "MANAGED_PARENT_RECOVERED") {
			break
		}
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if len(fixture.calls) != 2 || fixture.calls[parent] != 8 || fixture.calls[child] != 3 || fixture.child != child {
		t.Fatalf("cold recovery changed identity or repeated old work: %v", fixture.calls)
	}
}

func TestNativeThreadsServerProcessHelper(t *testing.T) {
	socket := os.Getenv("MEKUGI_NATIVE_THREADS_SOCKET")
	if socket == "" {
		t.Skip("isolated native-server subprocess")
	}
	certificate, err := base64.StdEncoding.DecodeString(os.Getenv("MEKUGI_NATIVE_THREADS_CERT"))
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(certificate)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialTLSContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&tls.Dialer{Config: &tls.Config{RootCAs: roots, ServerName: "example.com"}}).DialContext(ctx, network, os.Getenv("MEKUGI_NATIVE_THREADS_ADDRESS"))
	}
	http.DefaultTransport = transport
	defer transport.CloseIdleConnections()
	ctx, cancel := signal.NotifyContext(t.Context(), os.Interrupt)
	defer cancel()
	if code := RunServer(ctx, []string{"--socket", socket}, os.Stderr); code != 0 {
		t.Fatalf("server exit status: %d", code)
	}
}
