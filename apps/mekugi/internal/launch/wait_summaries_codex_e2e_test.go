//go:build codex_e2e

package launch

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/backends/codex/nativetrace"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/mcptransport"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	threadbinding "github.com/yusing/mekugi/modules/threadcontrol/codexbinding"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

type checkpointNativeProvider struct {
	failures                  chan error
	mu                        sync.Mutex
	parent, second, workspace string
	steps                     map[string]int
	selections                map[string]string
	sessions                  map[string]string
	turns                     map[string]string
	ready                     chan string
	parentYield               chan []byte
	continued                 chan string
	secondReply               chan struct{}
}

func (p *checkpointNativeProvider) ForwardExecution(ctx, _ context.Context, body []byte, headers http.Header, _ string) (response *inferencegateway.Response, err error) {
	defer func() {
		if err != nil {
			select {
			case p.failures <- err:
			default:
			}
		}
	}()
	metadata, ok := codexresponses.DecodeTurnMetadata(headers)
	if !ok {
		return nil, fmt.Errorf("checkpoint fixture lost native identity")
	}
	p.mu.Lock()
	p.steps[metadata.ThreadID]++
	step, parent, second := p.steps[metadata.ThreadID], p.parent, p.second
	previousTurn := p.turns[metadata.ThreadID]
	p.turns[metadata.ThreadID] = metadata.TurnID
	p.mu.Unlock()
	if metadata.ThreadID != parent && previousTurn != "" && previousTurn != metadata.TurnID {
		return nil, fmt.Errorf("checkpoint replaced the developer's native turn")
	}
	var history struct {
		Input []map[string]jsontext.Value `json:"input"`
	}
	if err := json.Unmarshal(body, &history); err != nil {
		return nil, err
	}
	for _, item := range history.Input {
		if string(item["role"]) == `"user"` && bytes.Contains(item["content"], []byte("PROGRESS_CHECKPOINT")) {
			return nil, fmt.Errorf("checkpoint polluted native user history")
		}
	}
	var program, answer string
	if metadata.ThreadID == parent {
		answer = "COORDINATOR_READY"
		if step == 2 {
			p.parentYield <- bytes.Clone(body)
			answer = "COMBINED_YIELD_RECEIVED"
		}
		if step > 2 {
			return nil, fmt.Errorf("duplicate parent wake")
		}
	} else {
		switch step {
		case 1:
			program = `text(await tools.exec_command({cmd:` + strconv.Quote("printf checkpoint-edit > value-"+metadata.ThreadID+".txt") + `,login:false}));`
		case 2:
			var request struct {
				Input []map[string]jsontext.Value `json:"input"`
			}
			if err := json.Unmarshal(body, &request); err != nil {
				return nil, err
			}
			selection := ""
			var toolOutputs strings.Builder
			for _, item := range request.Input {
				toolOutputs.Write(item["output"])
				for part, text := range binding.ExecutionOutputTexts(item["output"]) {
					if part == 0 {
						continue
					}
					line, _, _ := strings.Cut(text, "\n")
					for _, id := range strings.Fields(line) {
						if _, _, err := changes.ParseID(id); err == nil {
							selection = id
						}
					}
				}
			}
			if selection == "" {
				return nil, fmt.Errorf("native edit omitted its retained capture receipt: %.3000s", toolOutputs.String())
			}
			p.mu.Lock()
			p.selections[metadata.ThreadID] = selection
			p.mu.Unlock()
			command := "printf started >> checkpoint-started-" + metadata.ThreadID + "; read checkpoint_line; printf ended > checkpoint-ended-" + metadata.ThreadID + "; printf CHECKPOINT_TOOL_FINISHED"
			program = "// @exec: {\"yield_time_ms\":5000}\n" + `text(await tools.exec_command({cmd:` + strconv.Quote(command) + `,login:false,tty:true,yield_time_ms:30000}));`
			p.ready <- metadata.ThreadID
		case 3:
			// The request is queued while exec_command is still in flight. A
			// normal yielded session, not its background process exit, releases it.
			var request struct {
				Input []map[string]jsontext.Value `json:"input"`
			}
			if err := json.Unmarshal(body, &request); err != nil {
				return nil, err
			}
			var contextText strings.Builder
			for _, item := range request.Input {
				if raw := item["content"]; raw != nil && string(item["role"]) == `"assistant"` {
					if bytes.Contains(raw, []byte("PROGRESS_CHECKPOINT")) && (!bytes.Contains(raw, []byte("Sender: /root")) || !bytes.Contains(raw, []byte("Task name: /root/"))) {
						return nil, fmt.Errorf("checkpoint lost agent attribution: %s", raw)
					}
					var content []struct {
						Text string `json:"text"`
					}
					if json.Unmarshal(raw, &content) == nil {
						for _, part := range content {
							contextText.WriteString(part.Text)
						}
					}
				}
				for _, text := range binding.ExecutionOutputTexts(item["output"]) {
					contextText.WriteString(text)
				}
			}
			text := contextText.String()
			match := regexp.MustCompile(`MEKUGI_PROGRESS:([^\\\s"]+)`).FindStringSubmatch(text)
			cell := regexp.MustCompile(`cell ID ([^\s]+)`).FindStringSubmatch(text)
			p.mu.Lock()
			selection := p.selections[metadata.ThreadID]
			p.mu.Unlock()
			if len(match) != 2 || len(cell) != 2 || !strings.Contains(text, selection) || !strings.Contains(text, "longer replies are valid") || !strings.Contains(text, "completed tool results only") {
				return nil, fmt.Errorf("native checkpoint lost selection/request/continuation: route=%d cell=%d selection=%q text=%.4500s", len(match), len(cell), selection, text)
			}
			if _, err := os.Stat(filepath.Join(p.workspace, "checkpoint-ended-"+metadata.ThreadID)); err == nil {
				return nil, fmt.Errorf("fixture process unexpectedly ended before summary")
			}
			p.mu.Lock()
			p.sessions[metadata.ThreadID] = cell[1]
			p.mu.Unlock()
			if metadata.ThreadID != second {
				select {
				case <-p.secondReply:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			summary := "Changed the selected file to checkpoint-edit using the existing command path. Validation has not run."
			program = `text(await tools.mcp__threadcontrol__send_message({target:"agent:main",message:` + strconv.Quote(control.ProgressReplyPrefix+match[1]+"\n"+summary) + `}));`
		case 4:
			if !bytes.Contains(body, []byte("progress_recorded")) {
				return nil, fmt.Errorf("native developer reply did not reach the private checkpoint route")
			}
			p.mu.Lock()
			cell := p.sessions[metadata.ThreadID]
			p.mu.Unlock()
			args, _ := json.Marshal(map[string]any{"cell_id": cell, "yield_time_ms": 60000})
			return testGatewayResponse(mchangesNestedCodexResponse(step, map[string]any{"type": "function_call", "id": "checkpoint-wait-" + metadata.ThreadID, "call_id": "checkpoint-wait-" + metadata.ThreadID, "name": "wait", "status": "completed", "arguments": string(args)}), nil)
		case 5:
			var request struct {
				Input []map[string]jsontext.Value `json:"input"`
			}
			if err := json.Unmarshal(body, &request); err != nil {
				return nil, err
			}
			var outputs strings.Builder
			for _, item := range request.Input {
				for _, text := range binding.ExecutionOutputTexts(item["output"]) {
					outputs.WriteString(text)
				}
			}
			session := regexp.MustCompile(`(?i)(?:session_id|session ID)[^0-9]{0,12}([0-9]+)`).FindStringSubmatch(outputs.String())
			if len(session) != 2 {
				return nil, fmt.Errorf("original cell lost its native session handle: %.3000s", outputs.String())
			}
			program = `text(await tools.write_stdin({session_id:` + session[1] + `,chars:"continue\n",yield_time_ms:1000}));`
		case 6:
			if _, err := os.Stat(filepath.Join(p.workspace, "checkpoint-ended-"+metadata.ThreadID)); err != nil {
				return nil, fmt.Errorf("summary lost the original operation's continuation handle")
			}
			started, err := os.ReadFile(filepath.Join(p.workspace, "checkpoint-started-"+metadata.ThreadID))
			if err != nil || string(started) != "started" {
				return nil, fmt.Errorf("checkpoint replayed its active tool operation")
			}
			p.continued <- metadata.ThreadID
			answer = "ORIGINAL_ASSIGNMENT_CONTINUED"
		default:
			return nil, fmt.Errorf("unexpected developer turn %d", step)
		}
	}
	id := fmt.Sprintf("checkpoint-%s-%d", metadata.ThreadID, step)
	item := map[string]any{"type": "message", "id": id, "role": "assistant", "phase": "final_answer", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": answer}}}
	if program != "" {
		item = map[string]any{"type": "custom_tool_call", "id": id, "call_id": id, "name": "exec", "namespace": "functions", "input": program, "status": "completed"}
	}
	wire := routerFaultSSE(map[string]any{"type": "response.created", "response": map[string]any{"id": id, "status": "in_progress", "output": []any{}}}, map[string]any{"type": "response.output_item.added", "output_index": 0, "item": item}, map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item}, map[string]any{"type": "response.completed", "response": map[string]any{"id": id, "status": "completed", "output": []any{item}}})
	return testGatewayResponse(serverHTTPResponse(wire), nil)
}

type checkpointNativeHost struct {
	control.Native
	requests *sessioncontrol.Requests[struct{}]
	provider *checkpointNativeProvider
	tasks    map[string]string
}

func (h *checkpointNativeHost) Runtime(string) (control.Runtime, error) { return h.requests, nil }
func (h *checkpointNativeHost) Resume(context.Context, string) (control.Runtime, error) {
	return h.requests, nil
}
func (*checkpointNativeHost) Inspect(context.Context, string) (control.SessionDetails, error) {
	return control.SessionDetails{}, nil
}
func (*checkpointNativeHost) ObserveAdmitted(string, string, string, bool) bool { return true }
func (h *checkpointNativeHost) WaitProgress(_ string, seen map[string]int) (control.WaitProgress, map[string]int) {
	h.provider.mu.Lock()
	defer h.provider.mu.Unlock()
	progress, cursor := control.WaitProgress{}, make(map[string]int)
	for thread, id := range h.provider.selections {
		if seen[id] != 0 {
			continue
		}
		progress.Agents = append(progress.Agents, control.AgentProgress{Task: h.tasks[thread], Changes: id})
		cursor[id] = 1
	}
	return progress, cursor
}

func TestWaitSummariesSteerAndContinueNativeCodex(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	workspace := gitTestWorkspace(t)
	p := &checkpointNativeProvider{failures: make(chan error, 8), workspace: workspace, steps: make(map[string]int), selections: make(map[string]string), sessions: make(map[string]string), turns: make(map[string]string), ready: make(chan string, 2), parentYield: make(chan []byte, 2), continued: make(chan string, 2), secondReply: make(chan struct{})}
	proxy := newManagedMekugiProxy(t)
	attachTestReplayStore(t, proxy)
	traceRoot := t.TempDir()
	proxy.nativeTrace = nativetrace.NewWithNotice(traceRoot, nil)
	server := httptest.NewServer(inferencegateway.ResponsesHandler(ctx, time.Minute, testRequestExecutor(p, nil, proxy).ExecuteHTTP))
	defer server.Close()
	service := control.New()
	driver, err := threadbinding.NewDriver(service)
	if err != nil {
		t.Fatal(err)
	}
	coordination := mcp.NewServer(&mcp.Implementation{Name: "threadcontrol", Version: "1"}, nil)
	for _, tool := range threadbinding.ToolCatalog() {
		if tool.Name != "send_message" {
			continue
		}
		mcp.AddTool(coordination, tool, func(ctx context.Context, request *mcp.CallToolRequest, _ map[string]any) (*mcp.CallToolResult, any, error) {
			raw, _ := json.Marshal(request.Params.Meta)
			identity, err := binding.DecodeMCPIdentity(raw)
			if err != nil {
				return nil, nil, err
			}
			value, err := driver.Call(ctx, control.Identity{Thread: identity.Thread, Session: identity.Session, Turn: identity.Turn, Call: identity.Call, Item: identity.Item}, "send_message", request.Params.Arguments)
			if err == nil && identity.Thread == p.second {
				close(p.secondReply)
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(value)}}}, nil, err
		})
	}
	socket, stop, err := mcptransport.Start(ctx, coordination)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	config := fmt.Sprintf(`mcp_servers.threadcontrol={command=%q,args=["mcp-bridge",%q],enabled=true,omit_tools_from=["deferred"]}`, os.Args[0], socket)
	cmd := exec.CommandContext(ctx, "codex", "app-server", "-c", `model_providers.checkpoint={name="OpenAI",base_url=`+strconv.Quote(server.URL+"/v1")+`,wire_api="responses",requires_openai_auth=false}`, "-c", `model_provider="checkpoint"`, "-c", `model="gpt-6-astra"`, "-c", "features.plugins=false", "-c", "features.code_mode=true", "-c", "features.code_mode_host=true", "-c", "features.multi_agent_v2.enabled=true", "-c", "features.multi_agent_v2.disable_native_tools=true", "-c", config)
	cmd.Env, cmd.Dir = append(routerFaultCodexEnvironment(t), routerTestWorkerEnvironment+"=1", "CODEX_ROLLOUT_TRACE_ROOT="+traceRoot), workspace
	client, err := appserver.Start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	next := func() appserver.Message {
		t.Helper()
		select {
		case message := <-client.Messages:
			return message
		case <-ctx.Done():
			t.Fatalf("native timeout: %s", client.Diagnostics.Snapshot())
			return appserver.Message{}
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
	reply(client.Initialize())
	_, err = client.Send("initialized", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	paths := make(map[string]string)
	newThread := func() string {
		result := call("thread/start", map[string]any{"cwd": workspace, "model": "gpt-6-astra", "approvalPolicy": "never", "sandbox": "danger-full-access"})
		var thread struct {
			Thread struct {
				ID   string `json:"id"`
				Path string `json:"path"`
			} `json:"thread"`
		}
		if err := json.Unmarshal(result.Result, &thread); err != nil {
			t.Fatal(err)
		}
		paths[thread.Thread.ID] = thread.Thread.Path
		return thread.Thread.ID
	}
	parent, first, second := newThread(), newThread(), newThread()
	if err := proxy.retained.Store.Transaction(ctx, nil, func(tx *history.Transaction) error {
		if err := tx.InitializeScope(history.Scope{Thread: parent, Namespace: parent}, nil); err != nil {
			return err
		}
		for _, thread := range []string{first, second} {
			if err := tx.InitializeScope(history.Scope{Thread: thread, Namespace: parent, Parent: parent}, nil); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	p.parent, p.second = parent, second
	start := func(thread string) string {
		result := call("turn/start", map[string]any{"threadId": thread, "input": appserver.Input("Continue the original checkpoint fixture assignment.")})
		var turn struct {
			Turn struct {
				ID string `json:"id"`
			} `json:"turn"`
		}
		if err := json.Unmarshal(result.Result, &turn); err != nil {
			t.Fatal(err)
		}
		return turn.Turn.ID
	}
	parentTurn := start(parent)
	for {
		message := next()
		if message.Method == "turn/completed" {
			break
		}
	}
	histories, err := history.NewService(control.RecordKinds())
	if err != nil {
		t.Fatal(err)
	}
	store, err := histories.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	selected, err := store.SelectSession(ctx, nil, history.Limits{Total: history.DefaultStorageBytes}, parent)
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	check(service.AdmitRoot(ctx, selected, &control.Member{Thread: parent, Session: parent, Task: "/root", Turn: parentTurn, Generation: 1, Status: control.Working, HostRunning: true, AudienceAdmitted: true}))
	caller := control.Identity{Thread: parent, Session: parent, Turn: parentTurn, Call: "wait", Item: "wait"}
	for i, child := range []string{first, second} {
		turn := start(child)
		spawnCaller := caller
		spawnCaller.Call = fmt.Sprintf("spawn-%d", i)
		name := []string{"first", "second"}[i]
		spawn, err := service.ReserveSpawn(ctx, spawnCaller, control.SpawnRequest{Name: name, Message: "original assignment", Settings: control.Settings{Workspace: workspace}})
		check(err)
		for range 4 {
			effect, err := service.ClaimEffect(ctx, parent, spawn.ID)
			check(err)
			check(service.EffectAcknowledged(ctx, parent, spawn.ID, control.Receipt{Phase: effect.Phase, Thread: child, Session: child, Turn: turn}))
		}
	}
	requests := sessioncontrol.NewRequests[struct{}](client)
	defer requests.Close()
	host := &checkpointNativeHost{requests: requests, provider: p, tasks: map[string]string{first: "/root/first", second: "/root/second"}}
	check(driver.Connect(host, host))
	check(driver.Start(ctx))
	defer driver.Stop(context.Background())
	completed := make(chan string, 4)
	go func() {
		for {
			select {
			case call := <-requests.Calls():
				requests.Dispatch(call)
			case message, ok := <-client.Messages:
				if !ok {
					return
				}
				requests.CompleteCall(message)
				if (message.Method == "item/started" || message.Method == "item/completed") && bytes.Contains(message.Params, []byte("PROGRESS_CHECKPOINT")) && bytes.Contains(message.Params, []byte(`"type":"userMessage"`)) {
					p.failures <- fmt.Errorf("checkpoint emitted a native UserMessageItem")
				}
				if message.Method == "turn/completed" {
					var result struct {
						Thread string `json:"threadId"`
					}
					if json.Unmarshal(message.Params, &result) == nil && result.Thread != parent {
						completed <- result.Thread
					}
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	for range 2 {
		select {
		case <-p.ready:
		case err := <-p.failures:
			t.Fatal(err)
		case <-ctx.Done():
			t.Fatal("developer did not execute initial capture", client.Diagnostics.Snapshot())
		}
	}
	// Await actual command start, not provider generation. Both commands remain
	// blocked on their PTYs until the developers reuse the returned session IDs.
	for _, child := range []string{first, second} {
		for {
			if _, err := os.Stat(filepath.Join(workspace, "checkpoint-started-"+child)); err == nil {
				break
			}
			select {
			case <-time.After(10 * time.Millisecond):
			case <-ctx.Done():
				t.Fatal("native command did not start")
			}
		}
	}
	historyCounts := func(thread string) (starts, users, checkpoints int) {
		raw, err := os.ReadFile(paths[thread])
		check(err)
		for line := range bytes.SplitSeq(raw, []byte("\n")) {
			if len(line) == 0 {
				continue
			}
			var record struct {
				Type    string                    `json:"type"`
				Payload map[string]jsontext.Value `json:"payload"`
			}
			check(json.Unmarshal(line, &record))
			if record.Type == "event_msg" {
				switch string(record.Payload["type"]) {
				case `"task_started"`:
					starts++
				}
				if bytes.Contains(line, []byte("PROGRESS_CHECKPOINT")) && string(record.Payload["type"]) == `"user_message"` {
					t.Fatal("checkpoint emitted a native user-message event")
				}
			}
			if record.Type == "response_item" && string(record.Payload["role"]) == `"user"` {
				users++
			}
			if record.Type == "response_item" && bytes.Contains(record.Payload["content"], []byte("Message Type: PROGRESS_CHECKPOINT")) {
				checkpoints++
				if string(record.Payload["role"]) != `"assistant"` || !bytes.Contains(line, []byte("multi_agent.inter_agent_message")) {
					t.Fatalf("retained checkpoint lost assistant provenance: %.2500s", line)
				}
			}
		}
		return
	}
	baselineUsers := make(map[string]int)
	for _, child := range []string{first, second} {
		starts, users, checkpoints := historyCounts(child)
		if starts != 1 || checkpoints != 0 {
			t.Fatalf("invalid initial native history: starts=%d checkpoints=%d", starts, checkpoints)
		}
		baselineUsers[child] = users
	}
	now := time.Now()
	_, err = service.RegisterWait(ctx, caller, control.WaitAll, nil, new(int64(300000)), now.Add(-5*time.Minute))
	check(err)
	check(service.Execution(ctx, parent, parentTurn, caller.Item, "", true))
	_, err = service.ClaimLocalStop(ctx, parent, parentTurn, caller.Item)
	check(err)
	check(service.HostCompleted(ctx, parent, parentTurn, control.Completed, control.HostFailure{}))
	driver.Tick(now, []string{parent})
	var combined []byte
	select {
	case combined = <-p.parentYield:
	case err := <-p.failures:
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal("combined native yield missing", driver.Failures(), client.Diagnostics.Snapshot())
	}
	if bytes.Count(combined, []byte("Changed the selected file to checkpoint-edit")) != 2 || !bytes.Contains(combined, []byte("Validation has not run")) {
		t.Fatalf("combined yield lost developer-authored replies: %.3000s", combined)
	}
	p.mu.Lock()
	for _, id := range p.selections {
		if !bytes.Contains(combined, []byte(id)) {
			t.Errorf("combined yield omitted exact capture %s", id)
		}
	}
	p.mu.Unlock()
	for range 2 {
		select {
		case <-p.continued:
		case err := <-p.failures:
			t.Fatal(err)
		case <-ctx.Done():
			t.Fatal("developer lost original continuation", client.Diagnostics.Snapshot())
		}
	}
	for range 2 {
		select {
		case <-completed:
		case err := <-p.failures:
			t.Fatal(err)
		case <-ctx.Done():
			t.Fatal("native final completion missing")
		}
	}
	for _, child := range []string{first, second} {
		// The same native owner must retain a late checkpoint without waking idle
		// work. Its acknowledgement flushes the retained item before inspection.
		_, err := requests.Input(ctx, control.NativeInput{Thread: child, ID: "idle-checkpoint", Sender: "/root", Recipient: host.tasks[child], Kind: "PROGRESS_CHECKPOINT", Message: "Abandoned idle checkpoint."})
		check(err)
		starts, users, checkpoints := historyCounts(child)
		if starts != 1 || users != baselineUsers[child] || checkpoints != 2 {
			t.Fatalf("checkpoint polluted native history: starts=%d users=%d checkpoints=%d", starts, users, checkpoints)
		}
		member, _ := service.Member(child)
		if member.Generation != 1 || member.LastAnswer != "" {
			t.Fatal("summary was treated as an assignment outcome")
		}
	}
	select {
	case err := <-p.failures:
		t.Fatal(err)
	case <-p.parentYield:
		t.Fatal("individual reply independently woke parent")
	default:
	}
	cancel()
}
