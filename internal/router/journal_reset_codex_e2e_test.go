//go:build journal_e2e

package router

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
	p.turns++
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

// Uses the installed app-server and the same reset policy as native/headless UI.
// Only the local mock provider is contacted; compaction must be router-answered.
func TestJournalSliceResetNativeCodexE2E(t *testing.T) {
	ctx, cmd, provider := journalResetCodexFixture(t)
	testJournalSliceResetNativeCodex(t, ctx, cmd, provider)
}

func TestJournalSliceResetBuiltInOpenAINativeCodexE2E(t *testing.T) {
	ctx, cmd, provider := journalResetCodexFixtureProvider(t, true)
	testJournalSliceResetNativeCodex(t, ctx, cmd, provider)
	testJournalV2NativeResumeAndFork(t, ctx, cmd, provider)
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
