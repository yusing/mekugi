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
	"os/exec"
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
}

func (p *journalResetCodexProvider) forwardExecution(ctx, _ context.Context, body []byte, headers http.Header, _ string) (*http.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	metadata, _ := decodeCodexTurnMetadata(headers)
	if metadata.RequestKind == "compaction" {
		p.compactions++
		return nil, fmt.Errorf("journal reset compaction reached mock provider")
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
				p.recovered = p.recovered || strings.Contains(part.Text, "Journal recovery") && strings.Contains(part.Text, "/1 [done] First") && strings.Contains(part.Text, "/2 [pending] Second")
			}
		}
		if !p.continuation || p.proxy.journalCompaction != "off" && !p.recovered {
			return nil, fmt.Errorf("continuation missing path or durable recovery: path=%t recovery=%t", p.continuation, p.recovered)
		}
		if _, err := p.proxy.journals.apply(ctx, p.proxy.replayStore, p.workspace, thread, "fixture-second", []journalMutation{{Op: "set", P: "/2", State: new("done")}}); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unexpected model turn %d", p.turns)
	}
	return routerFaultCodexSuccessResponse(), nil
}

// Uses the installed app-server and the same reset policy as native/headless UI.
// Only the local mock provider is contacted; compaction must be router-answered.
func TestJournalSliceResetNativeCodexE2E(t *testing.T) {
	ctx, cmd, provider := journalResetCodexFixture(t)
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
				if event.ThreadID != thread || event.Turn.Status != "completed" {
					continue
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
			t.Fatalf("slice reset timed out: %v; notice=%s", ctx.Err(), notice)
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
	server := httptest.NewServer(responsesHandler(t.Context(), time.Minute, provider, nil, proxy))
	t.Cleanup(server.Close)
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, codex, "app-server", "-c", `model_providers.reset_fixture={name="reset_fixture",base_url=`+strconv.Quote(server.URL+"/v1")+`,wire_api="responses",requires_openai_auth=false}`, "-c", `model_provider="reset_fixture"`, "-c", `model="gpt-6-astra"`, "-c", "features.plugins=false", "-c", "features.goals=false", "-c", "include_collaboration_mode_instructions=false")
	cmd.Env = routerFaultCodexEnvironment(t)
	cmd.Dir = workspace
	return ctx, cmd, provider
}
