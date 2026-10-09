//go:build journal_e2e

package router

import (
	"bytes"
	"context"
	jsonv1 "encoding/json"
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
	"sync"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/appserver"
)

type journalResetCodexProvider struct {
	mu                      sync.Mutex
	proxy                   *mekugiProxy
	workspace               string
	turns, compactions      int
	continuation, recovered bool
	thread, retainedSummary string
	continuity              []codexTurnMetadata
	guidanceTool            string
	guidanceReadSent        bool
	retainedGuidance        string
	guidanceWithoutTool     int
}

func (p *journalResetCodexProvider) forwardExecution(ctx, _ context.Context, body []byte, headers http.Header, _ string) (*http.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	metadata, _ := decodeCodexTurnMetadata(headers)
	if metadata.RequestKind == "compaction" {
		p.compactions++
		return nil, fmt.Errorf("journal reset compaction reached mock provider")
	}
	if bytes.Contains(body, []byte(journalCompactionReferencePrefix)) {
		return nil, fmt.Errorf("local journal reference reached mock provider")
	}
	thread := headers.Get(threadIDHeader)
	if thread == "" {
		thread = metadata.ThreadID
	}
	if thread == "" {
		return nil, fmt.Errorf("native turn has no thread identity")
	}
	if p.guidanceTool != "" && p.turns == 0 {
		if !p.guidanceReadSent {
			p.guidanceReadSent = true
			item := map[string]any{"type": p.guidanceTool, "id": "guidance-read-item", "call_id": "guidance-read", "status": "completed"}
			if p.guidanceTool == "custom_tool_call" {
				item["name"], item["input"] = "exec", `const result = await tools.exec_command({cmd:"cat GUIDE.md"}); if (result.exit_code !== 0) throw new Error("guidance read failed"); text(result.output);`
			} else {
				item["name"], item["arguments"] = nativeExecCommandToolName, `{"cmd":"cat GUIDE.md"}`
			}
			return mchangesNestedCodexResponse(0, item), nil
		}
		var request struct {
			Input []map[string]jsonv1.RawMessage `json:"input"`
		}
		if err := json.Unmarshal(body, &request); err != nil {
			return nil, err
		}
		loaded := false
		for _, item := range request.Input {
			loaded = loaded || jsonString(item, "call_id") == "guidance-read" && strings.HasSuffix(jsonString(item, "type"), "_output") && strings.Contains(journalMessageText(item["output"]), "Guidance before reset.")
		}
		if !loaded {
			return nil, fmt.Errorf("native host did not return the real guidance read")
		}
		if err := os.WriteFile(filepath.Join(p.workspace, "GUIDE.md"), []byte("Guidance at reset.\n"), 0o600); err != nil {
			return nil, err
		}
	}
	p.turns++
	if p.guidanceTool != "" && p.turns > 1 {
		if err := p.checkGuidance(body); err != nil {
			return nil, err
		}
	}
	switch p.turns {
	case 1:
		p.thread = thread
		_, err := p.proxy.journals.apply(ctx, p.proxy.replayStore, p.workspace, thread, "fixture-plan", []journalMutation{{
			Op: "plan", Reset: "slice", Tasks: []jsontext.Value{jsontext.Value(`{"title":"First","state":"working"}`), jsontext.Value(`"Second"`)},
		}})
		if err != nil {
			return nil, err
		}
		if _, err := p.proxy.journals.apply(ctx, p.proxy.replayStore, p.workspace, thread, "fixture-first", []journalMutation{{Op: "set", P: "/1", State: new("done")}}); err != nil {
			return nil, err
		}
	case 2:
		var request struct {
			Input []struct {
				Role    string `json:"role"`
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"input"`
		}
		if err := json.Unmarshal(body, &request); err != nil {
			return nil, err
		}
		for _, item := range request.Input {
			for _, part := range item.Content {
				p.continuation = p.continuation || item.Role == "user" && strings.Contains(part.Text, "Continue the journal plan: /2 Second.")
				p.recovered = p.recovered || strings.Contains(part.Text, "Journal recovery") && strings.Contains(part.Text, `journal({op:"read",view:"outline"})`) && strings.Contains(part.Text, "/2 [pending] Second")
			}
		}
		if !p.continuation || p.proxy.journalCompaction != "off" && !p.recovered {
			return nil, fmt.Errorf("continuation missing path or durable recovery: path=%t recovery=%t", p.continuation, p.recovered)
		}
		if _, err := p.proxy.journals.apply(ctx, p.proxy.replayStore, p.workspace, thread, "fixture-second", []journalMutation{{Op: "set", P: "/2", State: new("done")}}); err != nil {
			return nil, err
		}
	default:
		if p.retainedSummary == "" {
			return nil, fmt.Errorf("unexpected model turn %d", p.turns)
		}
		var request struct {
			Input []struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"input"`
		}
		if err := json.Unmarshal(body, &request); err != nil {
			return nil, err
		}
		found := false
		for _, item := range request.Input {
			for _, part := range item.Content {
				found = found || part.Text == p.retainedSummary
			}
		}
		if !found {
			return nil, fmt.Errorf("native continuity lost exact retained reset summary")
		}
		p.continuity = append(p.continuity, metadata)
	}
	return routerFaultCodexSuccessResponse(), nil
}

func (p *journalResetCodexProvider) checkGuidance(body []byte) error {
	var request struct {
		Input []map[string]jsonv1.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		return err
	}
	wantName := "exec"
	if p.guidanceTool == "function_call" {
		wantName = nativeExecCommandToolName
	}
	calls := 0
	for i, item := range request.Input {
		if jsonString(item, "type") != p.guidanceTool || !strings.HasPrefix(jsonString(item, "call_id"), journalGuidanceCallID) {
			continue
		}
		calls++
		if i == 0 || i+1 >= len(request.Input) || !strings.Contains(journalMessageText(request.Input[i-1]["content"]), "Journal recovery") {
			return fmt.Errorf("synthetic guidance call is not immediately after recovery")
		}
		output := request.Input[i+1]
		text := jsonString(output, "output")
		if p.guidanceTool == "custom_tool_call" {
			var parts []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if err := json.Unmarshal(output["output"], &parts); err != nil || len(parts) != 1 || parts[0].Type != "input_text" {
				return fmt.Errorf("synthetic guidance lost its tool-output text part")
			}
			text = parts[0].Text
		}
		if jsonString(item, "name") != wantName || jsonString(output, "type") != p.guidanceTool+"_output" || jsonString(output, "call_id") != jsonString(item, "call_id") || text != p.retainedGuidance {
			return fmt.Errorf("synthetic guidance lost call identity or fresh exact output")
		}
		command := jsonString(item, "input") + jsonString(item, "arguments")
		if !strings.Contains(command, "cat ") || !strings.Contains(command, filepath.Join(p.workspace, "GUIDE.md")) {
			return fmt.Errorf("synthetic guidance lost its source command")
		}
	}
	if calls != 1 {
		return fmt.Errorf("expected one synthetic guidance pair, got %d", calls)
	}
	parsed, err := parseResponsesRequest(body)
	if err != nil {
		return err
	}
	catalog := parsed.responseTools()
	sections := []*responsesToolSection{catalog.top}
	for _, group := range catalog.additional {
		sections = append(sections, group.tools)
	}
	for len(sections) > 0 {
		section := sections[0]
		sections = sections[1:]
		if section.err != nil {
			return section.err
		}
		for _, tool := range section.tools {
			if tool.Name == wantName || tool.Name == "functions."+wantName {
				return nil
			}
			if tool.nested != nil {
				sections = append(sections, tool.nested)
			}
		}
	}
	if p.retainedSummary != "" {
		p.guidanceWithoutTool++
	}
	return nil
}

// Uses the installed app-server and the same reset policy as native/headless UI.
// Only the local mock provider is contacted; compaction must be router-answered.
func TestJournalSliceResetNativeCodexE2E(t *testing.T) {
	ctx, cmd, provider := journalResetCodexFixture(t)
	testJournalSliceResetNativeCodex(t, ctx, cmd, provider)
}

func TestJournalSliceResetBuiltInOpenAINativeCodexE2E(t *testing.T) {
	for _, tool := range []string{"custom_tool_call", "function_call"} {
		t.Run(tool, func(t *testing.T) {
			ctx, cmd, provider := journalResetCodexFixtureProvider(t, true)
			provider.guidanceTool = tool
			writeTestFile(t, filepath.Join(provider.workspace, "AGENTS.md"), "Read GUIDE.md.\n")
			writeTestFile(t, filepath.Join(provider.workspace, "GUIDE.md"), "Guidance before reset.\n")
			provider.retainedGuidance = journalGuidanceHeader + "\nFile: " + filepath.Join(provider.workspace, "GUIDE.md") + "\nGuidance at reset.\n"
			testJournalSliceResetNativeCodex(t, ctx, cmd, provider)
			testJournalV2NativeResumeAndFork(t, ctx, cmd, provider)
			// This native catalog exposes exec, but only its nested exec_command.
			wantAbsent := 0
			if tool == "function_call" {
				wantAbsent = 2
			}
			if provider.guidanceWithoutTool != wantAbsent {
				t.Fatalf("resume/fork tool-declaration coverage: got %d absent, want %d", provider.guidanceWithoutTool, wantAbsent)
			}
		})
	}
}

func testJournalV2NativeResumeAndFork(t *testing.T, ctx context.Context, prior *exec.Cmd, provider *journalResetCodexProvider) {
	t.Helper()
	store, err := openMekugiReplayStore(provider.proxy.replayStore.directory)
	if err != nil {
		t.Fatal(err)
	}
	provider.proxy.replayStore = store
	data, err := os.ReadFile(filepath.Join(store.directory, journalCompactionName(provider.workspace, provider.thread)))
	if err != nil {
		t.Fatal(err)
	}
	var receipt journalCompactionRecord
	if err := json.Unmarshal(data, &receipt); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(filepath.Join(store.directory, journalCompactionRecoveryName(provider.workspace, provider.thread, receipt.ResponseID)))
	if err != nil {
		t.Fatal(err)
	}
	var recovery journalCompactionRecovery
	if err := json.Unmarshal(data, &recovery); err != nil || recovery.Text == "" {
		t.Fatalf("native reset recovery unavailable: %v", err)
	}
	provider.retainedSummary = recovery.Text
	if provider.guidanceTool != "" && (recovery.Guidance == nil || recovery.Guidance.Tool != provider.guidanceTool || recovery.Guidance.Text != provider.retainedGuidance) {
		t.Fatal("reopened storage lost exact guidance snapshot")
	}
	cmd := exec.CommandContext(ctx, prior.Path, prior.Args[1:]...)
	cmd.Env, cmd.Dir = prior.Env, prior.Dir
	client, err := appserver.Start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { client.Close(); <-client.Done }()
	id, err := client.Initialize()
	if err != nil {
		t.Fatal(err)
	}
	awaitHistoryRPC(t, client, id)
	if _, err := client.Send("initialized", map[string]any{}, false); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"thread/resume", "thread/fork"} {
		id, err := client.Send(method, map[string]any{"threadId": provider.thread}, true)
		if err != nil {
			t.Fatal(err)
		}
		result := awaitHistoryRPC(t, client, id)
		var resumed struct {
			Thread appServerThreadInfo `json:"thread"`
		}
		if err := json.Unmarshal(result.Result, &resumed); err != nil || resumed.Thread.ID == "" {
			t.Fatalf("%s identity unavailable: %v", method, err)
		}
		id, err = client.Send("turn/start", map[string]any{"threadId": resumed.Thread.ID, "input": appserver.Input("Continue with the retained reset facts.")}, true)
		if err != nil {
			t.Fatal(err)
		}
		awaitHistoryRPC(t, client, id)
		completed := false
		for !completed {
			select {
			case message, ok := <-client.Messages:
				if !ok {
					t.Fatal("host closed during native continuity turn")
				}
				if message.Method != "turn/completed" {
					continue
				}
				var event struct {
					Thread string `json:"threadId"`
					Turn   struct {
						Status string `json:"status"`
					} `json:"turn"`
				}
				if err := json.Unmarshal(message.Params, &event); err != nil {
					t.Fatal(err)
				}
				if event.Thread == resumed.Thread.ID {
					if event.Turn.Status != "completed" {
						t.Fatalf("%s turn failed: %s", method, message.Params)
					}
					completed = true
				}
			case <-ctx.Done():
				t.Fatalf("%s continuity timed out: %v", method, ctx.Err())
			}
		}
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if len(provider.continuity) != 2 || provider.continuity[0].ThreadID != provider.thread || provider.continuity[1].ThreadID == provider.thread || provider.continuity[1].ForkedFromThreadID != provider.thread || provider.compactions != 0 {
		t.Fatalf("native resume/fork identity lost: %+v compactions=%d", provider.continuity, provider.compactions)
	}
}

func testJournalSliceResetNativeCodex(t *testing.T, ctx context.Context, cmd *exec.Cmd, provider *journalResetCodexProvider) {
	t.Helper()
	proxy, workspace := provider.proxy, provider.workspace
	client, err := appserver.Start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	exited := false
	defer func() {
		client.Close()
		if !exited {
			<-client.Done
		}
	}()
	initializeID, err := client.Initialize()
	if err != nil {
		t.Fatal(err)
	}
	var thread, startID, turnStartID string
	var firstDone, secondDone bool
	var driver *journalResetDriver
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for !secondDone || driver != nil && driver.active() {
		select {
		case m, ok := <-client.Messages:
			if !ok {
				t.Fatal("app-server closed before slice continuation")
			}
			if m.Method == "error" {
				t.Logf("native error: %s", m.Params)
			}
			if driver != nil {
				if handled, err := driver.message(m); err != nil {
					t.Fatalf("reset driver %s: %v", m.Method, err)
				} else if handled {
					continue
				}
			}
			if m.Method == "" {
				if m.Error != nil {
					t.Fatalf("app-server RPC: %s", m.Error.Message)
				}
				switch string(m.ID) {
				case initializeID:
					if _, err := client.Send("initialized", map[string]any{}, false); err != nil {
						t.Fatal(err)
					}
					startID, err = client.Send("thread/start", map[string]any{"approvalPolicy": "never", "sandbox": "danger-full-access"}, true)
					if err != nil {
						t.Fatal(err)
					}
				case startID:
					var result struct {
						Thread struct {
							ID string `json:"id"`
						} `json:"thread"`
					}
					if err := json.Unmarshal(m.Result, &result); err != nil {
						t.Fatal(err)
					}
					thread = result.Thread.ID
					if thread == "" {
						t.Fatal("thread/start returned no thread ID")
					}
					driver = &journalResetDriver{ctx: ctx, proxy: proxy, client: client, workspace: workspace, thread: thread, delay: 0}
					turnStartID, err = client.Send("turn/start", map[string]any{"threadId": thread, "input": appserver.Input("Complete the first journal slice.")}, true)
					if err != nil {
						t.Fatal(err)
					}
				case turnStartID:
					turnStartID = ""
				}
			}
			if m.Method == "turn/completed" && thread != "" {
				var event struct {
					ThreadID string `json:"threadId"`
					Turn     struct {
						ID     string `json:"id"`
						Status string `json:"status"`
					} `json:"turn"`
				}
				if err := json.Unmarshal(m.Params, &event); err != nil {
					t.Fatal(err)
				}
				if event.ThreadID != thread {
					continue
				}
				if event.Turn.Status != "completed" {
					t.Fatalf("native turn failed: %s", m.Params)
				}
				provider.mu.Lock()
				turns := provider.turns
				provider.mu.Unlock()
				if turns == 1 && !firstDone {
					firstDone = true
					if err := driver.completed(event.Turn.ID); err != nil {
						t.Fatal(err)
					}
				} else if turns == 2 {
					secondDone = true
				}
			}
		case <-ticker.C:
			if driver != nil {
				if err := driver.tick(time.Now()); err != nil {
					t.Fatal(err)
				}
			}
		case err := <-client.Done:
			exited = true
			t.Fatalf("app-server exited before reset: %v", err)
		case <-ctx.Done():
			var notice string
			if driver != nil {
				notice = driver.notice
			}
			provider.mu.Lock()
			turns, compactions := provider.turns, provider.compactions
			provider.mu.Unlock()
			t.Fatalf("slice reset timed out: %v; notice=%s turns=%d compactions=%d firstDone=%t", ctx.Err(), notice, turns, compactions, firstDone)
		}
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if !firstDone || provider.turns != 2 || provider.compactions != 0 || !provider.continuation || !provider.recovered {
		t.Fatalf("reset outcome: first=%t turns=%d compactions=%d continuation=%t recovered=%t", firstDone, provider.turns, provider.compactions, provider.continuation, provider.recovered)
	}
	intent, err := proxy.replayStore.resetIntent(ctx, workspace, thread)
	if err != nil || intent != nil {
		t.Fatalf("reset intent not consumed: %+v %v", intent, err)
	}
	if bytes.Contains([]byte(driver.notice), []byte("unavailable")) {
		t.Fatalf("reset driver degraded: %s", driver.notice)
	}
}

func journalResetCodexFixture(t *testing.T) (context.Context, *exec.Cmd, *journalResetCodexProvider) {
	t.Helper()
	return journalResetCodexFixtureProvider(t, false)
}

func journalResetCodexFixtureProvider(t *testing.T, builtIn bool) (context.Context, *exec.Cmd, *journalResetCodexProvider) {
	t.Helper()
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	workspace := t.TempDir()
	if output, err := exec.Command("git", "init", "--quiet", workspace).CombinedOutput(); err != nil {
		t.Fatalf("initialize fixture workspace: %v: %s", err, output)
	}
	proxy := newManagedMekugiProxy(t)
	attachTestReplayStore(t, proxy)
	proxy.journalCompaction = "slice"
	provider := &journalResetCodexProvider{proxy: proxy, workspace: workspace}
	handler := responsesHandler(t.Context(), time.Minute, provider, nil, proxy)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			http.Error(w, "fixture requires HTTP", http.StatusUpgradeRequired)
			return
		}
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, codex, "app-server", "-c", `model_providers.reset_fixture={name="reset_fixture",base_url=`+strconv.Quote(server.URL+"/v1")+`,wire_api="responses",requires_openai_auth=false}`, "-c", `model_provider="reset_fixture"`, "-c", `model="gpt-6-astra"`, "-c", "features.plugins=false", "-c", "features.goals=false", "-c", "include_collaboration_mode_instructions=false")
	cmd.Dir = workspace
	if builtIn {
		cmd.Args[3] = "openai_base_url=" + strconv.Quote(server.URL+"/v1")
		cmd.Args[5] = `model_provider="openai"`
		cmd.Env = builtInOpenAICodexEnvironment(t)
	} else {
		cmd.Env = routerFaultCodexEnvironment(t)
	}
	return ctx, cmd, provider
}
