//go:build journal_e2e

package router

import (
	"context"
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
)

// The installed app-server does not promise thread/started for a native child.
// Its own notifications and an observational thread/read must still resolve the
// canonical task name, role, and tool activity in the shared roster.
func TestAppServerChildMetadataNativeCodexE2E(t *testing.T) {
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	provider := &appServerChildMetadataProvider{turns: make(map[string]int)}
	server := httptest.NewServer(responsesHandler(t.Context(), time.Minute, provider, nil, nil, nil))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, codex, "app-server",
		"-c", `model_providers.child_meta={name="child_meta",base_url=`+strconv.Quote(server.URL+"/v1")+`,wire_api="responses",requires_openai_auth=false}`,
		"-c", `model_provider="child_meta"`, "-c", `model="gpt-6-astra"`,
		"-c", `features.multi_agent_v2={enabled=true,tool_namespace="collaboration"}`,
		"-c", "tools.update_plan.enabled=false", "-c", "include_collaboration_mode_instructions=false")
	cmd.Env = routerFaultCodexEnvironment(t)
	cmd.Dir = t.TempDir()
	client, err := startAppServer(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer client.close()
	u := &appServerUI{client: client, view: newLiveActivityView(), agents: newLiveActivityView(), requests: make(map[string]string), ctx: ctx}
	u.ensureShell()
	defer u.shell.diff.close()
	defer u.shell.diffScreen.Close()
	if err := u.request("initialize", nil); err != nil {
		t.Fatal(err)
	}
	started := false
	for {
		select {
		case m, ok := <-client.messages:
			if !ok {
				t.Fatal("app-server closed before child activity")
			}
			if err := u.message(m); err != nil {
				t.Fatalf("app-server %s: %v", m.Method, err)
			}
			if u.thread != "" && !started {
				started = true
				if err := u.request("turn/start", map[string]any{"threadId": u.thread, "input": appServerInput("Spawn the assigned child.")}); err != nil {
					t.Fatal(err)
				}
			}
			for thread, path := range u.session.paths {
				requestID, observed := u.session.metadata[thread]
				if thread == u.thread || path != "/root/metadata_probe" || !observed || requestID != "" {
					continue
				}
				agent := u.session.agent(path)
				if agent == nil || agent.Role != "explorer" {
					continue
				}
				for _, entry := range u.agents.entries {
					if entry.Agent == path && entry.Kind == "tool" && strings.Contains(entry.Text, "printf CHILD_METADATA_TOOL") {
						for _, view := range []*liveActivityView{u.view, u.agents} {
							found := false
							for _, summary := range view.entries {
								found = found || summary.Kind == "reasoning" && summary.Text == "Checking the native fixture."
							}
							if !found {
								t.Fatal("real app-server public summary missing")
							}
						}
						return
					}
				}
			}
		case err := <-client.done:
			t.Fatalf("app-server exited before child metadata and tool activity: %v", err)
		case <-ctx.Done():
			t.Fatalf("child identity, role, or tool activity missing: %v; paths=%v agents=%+v entries=%+v", ctx.Err(), u.session.paths, u.session.agents, u.agents.entries)
		}
	}
}

type appServerChildMetadataProvider struct {
	mu    sync.Mutex
	turns map[string]int
}

func (p *appServerChildMetadataProvider) forwardExecution(_, _ context.Context, _ []byte, headers http.Header, _ string) (*http.Response, error) {
	metadata, _ := decodeCodexTurnMetadata(headers)
	thread := headers.Get(threadIDHeader)
	if thread == "" {
		thread = metadata.ThreadID
	}
	p.mu.Lock()
	p.turns[thread]++
	turn := p.turns[thread]
	p.mu.Unlock()
	if turn > 8 {
		return nil, fmt.Errorf("native child metadata fixture exceeded eight provider turns for thread %s", thread)
	}
	var item map[string]any
	call := func(name, namespace string, args any) map[string]any {
		return map[string]any{"type": "function_call", "id": fmt.Sprintf("fc_%s_%d", thread, turn), "call_id": fmt.Sprintf("call_%s_%d", thread, turn), "name": name, "namespace": namespace, "arguments": string(mustMarshalJSON(args)), "status": "completed"}
	}
	if metadata.SubagentKind != "" {
		if turn == 1 {
			item = call("exec_command", "functions", map[string]any{"cmd": "printf CHILD_METADATA_TOOL", "max_output_tokens": 100})
		} else {
			item = map[string]any{"type": "message", "id": fmt.Sprintf("answer_%s_%d", thread, turn), "role": "assistant", "phase": "final_answer", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "Child inspection complete."}}}
		}
	} else if turn == 1 {
		item = call("spawn_agent", "collaboration", map[string]any{"task_name": "metadata_probe", "agent_type": "explorer", "fork_turns": "none", "message": "Run the assigned command and report."})
	} else {
		item = call("wait_agent", "collaboration", map[string]any{"timeout_ms": 10000})
	}
	response := map[string]any{"id": fmt.Sprintf("resp_%s_%d", thread, turn), "status": "completed", "output": []any{item}, "usage": map[string]any{"input_tokens": 10, "output_tokens": 5, "total_tokens": 15}}
	events := []map[string]any{{"type": "response.created", "response": map[string]any{"id": response["id"], "status": "in_progress", "output": []any{}}}}
	index := 0
	if turn == 1 {
		reasonID := fmt.Sprintf("reason_%s", thread)
		summary := map[string]any{"type": "summary_text", "text": "Checking the native fixture."}
		reason := map[string]any{"id": reasonID, "type": "reasoning", "summary": []any{summary}}
		events = append(events,
			map[string]any{"type": "response.output_item.added", "output_index": 0, "item": map[string]any{"id": reasonID, "type": "reasoning", "summary": []any{}}},
			map[string]any{"type": "response.reasoning_summary_part.added", "item_id": reasonID, "summary_index": 0, "part": map[string]any{"type": "summary_text", "text": ""}},
			map[string]any{"type": "response.reasoning_summary_text.delta", "item_id": reasonID, "summary_index": 0, "delta": summary["text"]},
			map[string]any{"type": "response.output_item.done", "output_index": 0, "item": reason})
		response["output"] = []any{reason, item}
		index = 1
	}
	events = append(events, map[string]any{"type": "response.output_item.added", "output_index": index, "item": item})
	if item["type"] == "function_call" {
		events = append(events, map[string]any{"type": "response.function_call_arguments.done", "item_id": item["id"], "arguments": item["arguments"]})
	}
	events = append(events, map[string]any{"type": "response.output_item.done", "output_index": index, "item": item}, map[string]any{"type": "response.completed", "response": response})
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
