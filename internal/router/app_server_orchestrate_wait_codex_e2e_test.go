//go:build journal_e2e

package router

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yusing/mekugi/internal/appserver"
	"github.com/yusing/mekugi/internal/orchestrate"
)

type waitCodexProvider struct {
	mu        sync.Mutex
	calls     int
	timeout   int
	item      string
	gate      bool
	mismatch  string
	proxy     *mekugiProxy
	workspace string
}

func (p *waitCodexProvider) forwardExecution(ctx, _ context.Context, body []byte, headers http.Header, _ string) (*http.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if p.calls == 1 {
		args := `{}`
		if p.timeout != 0 {
			args = fmt.Sprintf(`{"timeout_ms":%d}`, p.timeout)
		}
		source := `const r = await tools.mcp__orchestrate__wait_agent(` + args + `); if (r.isError) throw new Error(JSON.stringify(r)); text(r);`
		if p.gate {
			source += ` await tools.mcp__orchestrate__wait_gate({});`
		}
		return mchangesNestedCodexResponse(1, map[string]any{"type": "custom_tool_call", "id": p.item, "call_id": "wait-call", "name": "exec", "status": "completed", "input": source}), nil
	}
	if p.calls != 2 || !strings.Contains(string(body), "WAIT_RESULT") {
		p.mismatch = fmt.Sprintf("unexpected inference %d: %.2000s", p.calls, body)
	}
	if p.timeout == 0 && !strings.Contains(string(body), "selected-message") || p.timeout != 0 && !strings.Contains(string(body), `timed_out`) {
		p.mismatch = fmt.Sprintf("selected result missing: %.2000s", body)
	}
	_, err := p.proxy.journals.apply(ctx, p.proxy.replayStore, p.workspace, codexThreadID(headers), "settled", []journalMutation{{Op: "set", P: "/1", State: new("done")}})
	if err != nil {
		return nil, err
	}
	return routerFaultCodexSuccessResponse(), nil
}

func TestOrchestrateWaitNativeCodexE2E(t *testing.T) {
	for _, tc := range []struct {
		item       string
		timeout    int
		interleave string
	}{{"ctc_wait", 0, ""}, {"ctc_wait", 5000, ""}, {"wait-item", 0, ""}, {"ctc_wait", 0, "claim"}, {"ctc_wait", 0, "input"}} {
		t.Run(tc.item+strconv.Itoa(tc.timeout)+tc.interleave, func(t *testing.T) {
			timeout := tc.timeout
			codex, err := exec.LookPath("codex")
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("CODEX_HOME", t.TempDir())
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			trace := t.TempDir()
			t.Setenv("CODEX_ROLLOUT_TRACE_ROOT", trace)
			workspace := t.TempDir()
			proxy := newManagedMekugiProxy(t)
			attachTestReplayStore(t, proxy)
			proxy.nativeTrace = &nativeToolTrace{directory: trace}
			store := &orchestrate.Store{Directory: t.TempDir()}
			gateEntered, gateRelease := make(chan struct{}), make(chan struct{})
			mcpServer := newOrchestrateMCPServer(proxy, store)
			if tc.interleave != "" {
				mcpServer.AddTool(&mcp.Tool{Name: "wait_gate", InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
					close(gateEntered)
					select {
					case <-gateRelease:
						return &mcp.CallToolResult{}, nil
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				})
			}
			socket, stop, err := startJournalMCP(t.Context(), mcpServer)
			if err != nil {
				t.Fatal(err)
			}
			defer stop()
			provider := &waitCodexProvider{timeout: timeout, item: tc.item, gate: tc.interleave != "", proxy: proxy, workspace: workspace}
			server := httptest.NewServer(responsesHandler(t.Context(), time.Minute, provider, nil, proxy))
			defer server.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, codex, "app-server", "-c", `model_providers.wait_test={name="wait_test",base_url=`+strconv.Quote(server.URL+"/v1")+`,wire_api="responses",requires_openai_auth=false}`, "-c", `model_provider="wait_test"`, "-c", `model="gpt-6-astra"`, "-c", "features.plugins=false", "-c", "features.code_mode=true", "-c", "features.code_mode_host=true", "-c", `approval_policy="never"`, "-c", `sandbox_mode="danger-full-access"`, "-c", `mcp_servers.orchestrate={command=`+strconv.Quote(os.Args[0])+`,args=["journal-mcp",`+strconv.Quote(socket)+`]}`)
			cmd.Env, cmd.Dir = routerFaultCodexEnvironment(t), workspace
			client, err := appserver.Start(cmd)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			u := &appServerUI{ctx: ctx, client: client, proxy: proxy, view: newLiveActivityView(), agents: newLiveActivityView(), requests: make(map[string]string), notifications: &nativeNotifications{out: io.Discard}, resumeCwd: workspace}
			u.ensureShell()
			defer u.shell.diff.close()
			defer u.shell.diffScreen.Close()
			defer u.closeOrchestrateStorage()
			defer u.closeOrchestratedViews()
			now := time.Now()
			u.clock = func() time.Time { return now }
			completed := 0
			var heldComplete func()
			var entered <-chan struct{} = gateEntered
			gateReleased := false
			pump := func(done func() bool) {
				t.Helper()
				for !done() {
					select {
					case m, ok := <-client.Messages:
						if !ok {
							t.Fatal("host closed")
						}
						if err := u.message(m); err != nil {
							t.Fatal(err)
						}
						if m.Method == "turn/completed" {
							var event appServerEvent
							_ = json.Unmarshal(m.Params, &event)
							if event.Turn.Status != "completed" {
								t.Fatalf("host turn failed: %s", m.Params)
							}
							completed++
						}
					case command := <-u.orchestrateCommands():
						u.startOrchestratedChild(command)
					case <-entered:
						entered = nil
						u.advanceOrchestratedWaits(workspace, u.thread, []orchestrateEvent{{ID: "selected", Task: "batch", Thread: "child", Kind: "message", Message: "selected-message"}})
					case complete := <-u.orchestrateCompletions:
						if tc.interleave != "" && entered == nil && !gateReleased {
							state := "dispatching"
							if tc.interleave == "input" {
								state = "delivered"
							}
							waits, err := store.Waits(workspace, u.thread, state)
							if err != nil {
								t.Fatal(err)
							}
							if len(waits) != 0 {
								// Complete the exec either before sending selected input,
								// or after Codex accepted it into the still-active turn.
								gateReleased = true
								close(gateRelease)
								if tc.interleave == "claim" {
									heldComplete = complete
									continue
								}
							}
						}
						u.completeOrchestrateWork(complete)
					case <-ctx.Done():
						t.Fatalf("host timeout: %s", u.notice)
					}
				}
			}
			if err := u.request("initialize", nil); err != nil {
				t.Fatal(err)
			}
			pump(func() bool { return u.thread != "" && !u.modelsLoading })
			main := u.thread
			proxy.orchestration.active.Store(true)
			lease, release, err := proxy.replayStore.beginSession(ctx, main, "")
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			if err := proxy.journals.initialize(lease, proxy.replayStore, workspace, main, "/root", ""); err != nil {
				t.Fatal(err)
			}
			if err := proxy.journals.bindIdentity(lease, proxy.replayStore, workspace, main, "", "/root", true); err != nil {
				t.Fatal(err)
			}
			if _, err := proxy.journals.apply(lease, proxy.replayStore, workspace, main, "seed", []journalMutation{{Op: "add", Kind: "task", Title: new("Wait for selected batch"), State: new("working")}}); err != nil {
				t.Fatal(err)
			}
			if err := u.request("turn/start", map[string]any{"threadId": main, "input": []any{map[string]any{"type": "text", "text": "Wait for the selected event."}}}); err != nil {
				t.Fatal(err)
			}
			pump(func() bool { return completed == 1 })
			provider.mu.Lock()
			calls, mismatch := provider.calls, provider.mismatch
			provider.mu.Unlock()
			wantCalls := 1
			if tc.interleave == "input" {
				wantCalls = 2
			}
			if calls != wantCalls || mismatch != "" || u.reset.active() || u.turn != "" {
				t.Fatalf("wait did not suspend: calls=%d reset=%v turn=%s notice=%s mismatch=%s", calls, u.reset.active(), u.turn, u.notice, mismatch)
			}
			if tc.interleave == "input" {
				return
			}
			if heldComplete != nil {
				u.completeOrchestrateWork(heldComplete)
			} else {
				// Advance the real deadline owner without sleeping or making a model call.
				now = now.Add(24 * time.Hour)
				u.flushOrchestratedEvents()
				if timeout == 0 {
					drainOrchestrateWork(t, u)
					provider.mu.Lock()
					calls = provider.calls
					provider.mu.Unlock()
					if calls != 1 || u.reset.active() {
						t.Fatal("indefinite wait woke automatically")
					}
					u.advanceOrchestratedWaits(workspace, main, []orchestrateEvent{{ID: "selected", Task: "batch", Thread: "child", Kind: "message", Message: "selected-message"}})
				}
			}
			pump(func() bool { return completed == 2 })
			provider.mu.Lock()
			calls, mismatch = provider.calls, provider.mismatch
			provider.mu.Unlock()
			if calls != 2 || mismatch != "" {
				t.Fatalf("yield inferred %d times: %s", calls, mismatch)
			}
		})
	}
}
