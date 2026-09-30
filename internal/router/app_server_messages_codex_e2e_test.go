//go:build journal_e2e

package router

import (
	"context"
	jsonv1 "encoding/json"
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

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/appserver"
)

// A real Codex app-server exposes native child lifecycle, but not a public
// collabAgentToolCall item for send_message. The observed request is the only
// source for the full plaintext directed message and original assignment.
func TestAppServerDirectedChildMessageNativeCodexE2E(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	const assignment = "Inspect the child message and report one finding."
	message := "Child finding: " + strings.Repeat("the native message must remain complete and attributable; ", 12)
	if len(message) <= 512 {
		t.Fatal("message fixture must exceed the excerpt threshold")
	}
	provider := &appServerDirectedMessageProvider{turns: make(map[string]int), assignment: assignment, message: message}
	proxy := newManagedMekugiProxy(t)
	server := httptest.NewServer(responsesHandler(t.Context(), time.Minute, provider, nil, proxy))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, codex, "app-server",
		"-c", `model_providers.native_messages={name="native_messages",base_url=`+strconv.Quote(server.URL+"/v1")+`,wire_api="responses",requires_openai_auth=false}`,
		"-c", `model_provider="native_messages"`, "-c", `model="gpt-6-astra"`,
		"-c", `features.multi_agent_v2={enabled=true,tool_namespace="collaboration"}`,
		"-c", "tools.update_plan.enabled=false", "-c", "include_collaboration_mode_instructions=false")
	cmd.Env = routerFaultCodexEnvironment(t)
	cmd.Dir = t.TempDir()
	client, err := appserver.Start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	u := &appServerUI{client: client, proxy: proxy, view: newLiveActivityView(), agents: newLiveActivityView(), requests: make(map[string]string), ctx: ctx}
	u.ensureShell()
	defer u.shell.diff.close()
	defer u.shell.diffScreen.Close()
	if err := u.request("initialize", nil); err != nil {
		t.Fatal(err)
	}
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	started := false
	for {
		select {
		case m, ok := <-client.Messages:
			if !ok {
				t.Fatal("app-server closed before the directed message was rendered")
			}
			if err := u.message(m); err != nil {
				t.Fatalf("app-server %s: %v", m.Method, err)
			}
			u.applyObservedActivity()
			if u.thread != "" && !started {
				started = true
				if err := u.request("turn/start", map[string]any{"threadId": u.thread, "input": appserver.Input("Spawn the assigned child and wait for its report.")}); err != nil {
					t.Fatal(err)
				}
			}
		case <-tick.C:
			u.applyObservedActivity()
		case err := <-client.Done:
			t.Fatalf("app-server exited before message and reply link: %v", err)
		case <-ctx.Done():
			provider.mu.Lock()
			detail := fmt.Sprintf("turns=%v seen=%v input=%v", provider.turns, provider.messageSeen, provider.inputShapes)
			provider.mu.Unlock()
			t.Fatalf("native child message was not rendered: %v; status=%s %s", ctx.Err(), u.status, detail)
		}
		if started && provider.completed() {
			var assignmentCount, messageCount, excerptCount int
			var activitySeq uint64
			for _, entry := range u.agents.entries {
				if entry.assignment != nil && entry.assignment.text == assignment {
					assignmentCount++
				}
				if entry.Kind == "reply" && strings.Contains(entry.Text, message) {
					messageCount++
					activitySeq = entry.Seq
				}
			}
			for _, entry := range u.view.entries {
				if entry.Kind == "reply" && entry.activitySeq == activitySeq && activitySeq != 0 {
					excerptCount++
				}
			}
			if assignmentCount > 1 || messageCount > 1 || excerptCount > 1 {
				t.Fatal("native assignment or directed reply was displayed twice")
			}
			if assignmentCount != 1 || messageCount != 1 || excerptCount != 1 {
				continue // The authenticated observer may arrive after app-server completion.
			}
			u.view.conversation = true
			main := ansi.Strip(strings.Join(u.view.renderFeed(100, 60).lines, "\n"))
			if strings.Contains(main, "Message received:") {
				t.Fatalf("legacy received envelope leaked into Main:\n%s", main)
			}
			if strings.Count(main, "the native message must remain complete") > 3 || !strings.Contains(main, "↩ Open reply in Activity") {
				t.Fatalf("Main did not collapse/link the message: %s", main)
			}
			provider.mu.Lock()
			seen := provider.messageSeen
			provider.mu.Unlock()
			if !seen {
				t.Fatal("parent model did not receive the original full message")
			}
			return
		}
	}
}

type appServerDirectedMessageProvider struct {
	mu          sync.Mutex
	turns       map[string]int
	assignment  string
	message     string
	rootDone    bool
	messageSeen bool
	inputShapes []string
}

func (p *appServerDirectedMessageProvider) completed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.rootDone
}

func (p *appServerDirectedMessageProvider) forwardExecution(_, _ context.Context, body []byte, headers http.Header, _ string) (*http.Response, error) {
	metadata, _ := decodeCodexTurnMetadata(headers)
	thread := headers.Get(threadIDHeader)
	if thread == "" {
		thread = metadata.ThreadID
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.turns[thread]++
	turn := p.turns[thread]
	if metadata.SubagentKind == "" && strings.Contains(string(body), p.message) {
		p.messageSeen = true
	}
	{
		var request struct {
			Input []map[string]jsonv1.RawMessage `json:"input"`
		}
		if json.Unmarshal(body, &request) == nil {
			var shape []string
			for _, item := range request.Input {
				if jsonString(item, "type") == "function_call_output" {
					shape = append(shape, "result:"+string(item["output"]))
				}
			}
			p.inputShapes = append(p.inputShapes, metadata.AgentName+":"+strings.Join(shape, ","))
		}
	}
	if turn > 8 {
		return nil, fmt.Errorf("directed-message fixture exceeded eight turns for %s", thread)
	}
	call := func(name string, args any) map[string]any {
		return map[string]any{"type": "function_call", "id": fmt.Sprintf("fc_%s_%d", thread, turn),
			"call_id": fmt.Sprintf("call_%s_%d", thread, turn), "name": name, "namespace": subagentBridgeNamespace,
			"arguments": string(mustMarshalJSON(args)), "status": "completed"}
	}
	answer := func(text string) map[string]any {
		return map[string]any{"type": "message", "id": fmt.Sprintf("answer_%s_%d", thread, turn), "role": "assistant",
			"phase": "final_answer", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": text}}}
	}
	var item map[string]any
	if metadata.SubagentKind != "" {
		if turn == 1 {
			item = call("send_message", map[string]any{"target": "/root", "message": p.message})
		} else {
			item = answer("Child inspection complete.")
		}
	} else if turn == 1 {
		item = call("spawn_agent", map[string]any{"task_name": "message_worker", "fork_turns": "none", "message": p.assignment})
	} else if strings.Contains(string(body), "Child inspection complete.") {
		p.rootDone = true
		item = answer("Parent received the child report.")
	} else {
		item = call("wait_agent", map[string]any{"timeout_ms": 10000})
	}
	response := map[string]any{"id": fmt.Sprintf("resp_%s_%d", thread, turn), "status": "completed", "output": []any{item},
		"usage": map[string]any{"input_tokens": 10, "output_tokens": 5, "total_tokens": 15}}
	events := []any{
		map[string]any{"type": "response.created", "response": map[string]any{"id": response["id"], "status": "in_progress", "output": []any{}}},
		map[string]any{"type": "response.output_item.added", "output_index": 0, "item": item},
	}
	if item["type"] == "function_call" {
		events = append(events, map[string]any{"type": "response.function_call_arguments.done", "item_id": item["id"], "arguments": item["arguments"]})
	}
	events = append(events, map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item},
		map[string]any{"type": "response.completed", "response": response})
	var wire strings.Builder
	for _, event := range events {
		encoded, err := json.Marshal(event)
		if err != nil {
			return nil, err
		}
		wire.WriteString("data: ")
		wire.Write(encoded)
		wire.WriteString("\n\n")
	}
	result := serverHTTPResponse(wire.String())
	result.Header.Set("Content-Type", "text/event-stream")
	return result, nil
}
