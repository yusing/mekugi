//go:build journal_e2e

package router

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/appserver"
)

type journalAutoResumeCodexProvider struct {
	mu                 sync.Mutex
	proxy              *mekugiProxy
	workspace          string
	turns, compactions int
	continuation       bool
}

func (p *journalAutoResumeCodexProvider) forwardExecution(ctx, _ context.Context, body []byte, headers http.Header, _ string) (*http.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	metadata, _ := decodeCodexTurnMetadata(headers)
	if metadata.RequestKind == "compaction" {
		p.compactions++
		return nil, fmt.Errorf("ordinary journal continuation reached provider compaction")
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
		// Answer the follow-up without touching the pre-existing journal plan.
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
				p.continuation = p.continuation || item.Role == "user" && strings.Contains(part.Text, "Continue the journal plan: /1 Remaining.")
			}
		}
		if !p.continuation {
			return nil, fmt.Errorf("host continuation did not select the remaining journal task")
		}
		if _, err := p.proxy.journals.apply(ctx, p.proxy.replayStore, p.workspace, thread, "fixture-remaining", []journalMutation{{Op: "set", P: "/1", State: new("done")}}); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unexpected model turn %d", p.turns)
	}
	return routerFaultCodexSuccessResponse(), nil
}

// Ordinary pending and working tasks resume after a substantive answer even
// without a task transition. Slice mode must not compact a non-slice plan.
func TestJournalAutoResumeNativeCodexE2E(t *testing.T) {
	for _, state := range []string{"pending", "working"} {
		t.Run(state, func(t *testing.T) {
			ctx, cmd, provider := journalAutoResumeCodexFixture(t)
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
			var thread, startID string
			var driver *journalResetDriver
			completed := 0
			ticker := time.NewTicker(20 * time.Millisecond)
			defer ticker.Stop()
			for completed < 2 || driver != nil && driver.active() {
				select {
				case m, ok := <-client.Messages:
					if !ok {
						t.Fatal("app-server closed before ordinary continuation")
					}
					if driver != nil {
						if handled, err := driver.message(m); err != nil {
							t.Fatalf("continuation driver %s: %v", m.Method, err)
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
							// Seed before the host turn starts, so its successful answer
							// contains no journal task transition or slice completion.
							if err := proxy.journals.initialize(ctx, proxy.replayStore, workspace, thread, "/root", ""); err != nil {
								t.Fatal(err)
							}
							if _, err := proxy.journals.apply(ctx, proxy.replayStore, workspace, thread, "fixture-plan", []journalMutation{{Op: "plan", Tasks: []jsontext.Value{jsontext.Value(fmt.Sprintf(`{"title":"Remaining","state":%q}`, state))}}}); err != nil {
								t.Fatal(err)
							}
							driver = &journalResetDriver{ctx: ctx, proxy: proxy, client: client, workspace: workspace, thread: thread, delay: 0}
							if _, err := client.Send("turn/start", map[string]any{"threadId": thread, "input": appserver.Input("Answer this follow-up question without modifying the journal tasks.")}, true); err != nil {
								t.Fatal(err)
							}
						}
					}
					if m.Method == "item/completed" {
						var event struct {
							Item appServerItem `json:"item"`
						}
						if err := json.Unmarshal(m.Params, &event); err != nil {
							t.Fatal(err)
						}
						if event.Item.Type == "contextCompaction" {
							t.Fatal("ordinary plan was compacted in slice mode")
						}
					}
					if m.Method == "turn/completed" && driver != nil {
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
							t.Fatalf("native turn failed: %s", event.Turn.Status)
						}
						completed++
						if err := driver.completed(event.Turn.ID); err != nil {
							t.Fatal(err)
						}
						if completed == 1 && (driver.intent == nil || !driver.intent.Resume || driver.intent.Path != "/1") {
							t.Fatalf("follow-up did not schedule ordinary continuation: %+v", driver.intent)
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
					t.Fatalf("app-server exited before continuation: %v", err)
				case <-ctx.Done():
					t.Fatalf("ordinary continuation timed out: %v", ctx.Err())
				}
			}
			if err := driver.tick(time.Now().Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			if driver.active() || driver.intent != nil {
				t.Fatalf("completed plan scheduled another continuation: %+v", driver.intent)
			}
			items, err := proxy.journals.list(ctx, proxy.replayStore, workspace, thread)
			if err != nil || len(items) == 0 || items[0].Path != "/1" || items[0].State != "done" {
				t.Fatalf("remaining task was not durably completed: %+v %v", items, err)
			}
			intent, err := proxy.replayStore.resetIntent(ctx, workspace, thread)
			if err != nil || intent != nil {
				t.Fatalf("continuation intent not consumed: %+v %v", intent, err)
			}
			provider.mu.Lock()
			defer provider.mu.Unlock()
			if completed != 2 || provider.turns != 2 || provider.compactions != 0 || !provider.continuation {
				t.Fatalf("continuation outcome: completed=%d turns=%d compactions=%d continuation=%t", completed, provider.turns, provider.compactions, provider.continuation)
			}
		})
	}
}

func journalAutoResumeCodexFixture(t *testing.T) (context.Context, *exec.Cmd, *journalAutoResumeCodexProvider) {
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
	provider := &journalAutoResumeCodexProvider{proxy: proxy, workspace: workspace}
	server := httptest.NewServer(responsesHandler(t.Context(), time.Minute, provider, nil, proxy))
	t.Cleanup(server.Close)
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, codex, "app-server", "-c", `model_providers.auto_resume_fixture={name="auto_resume_fixture",base_url=`+strconv.Quote(server.URL+"/v1")+`,wire_api="responses",requires_openai_auth=false}`, "-c", `model_provider="auto_resume_fixture"`, "-c", `model="gpt-6-astra"`, "-c", "features.plugins=false", "-c", "features.goals=false", "-c", "include_collaboration_mode_instructions=false")
	cmd.Env = routerFaultCodexEnvironment(t)
	cmd.Dir = workspace
	return ctx, cmd, provider
}
