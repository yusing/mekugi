//go:build journal_e2e

package router

import (
	"bytes"
	"context"
	"encoding/json"
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

// This fixture uses the installed Codex consumer and native collaboration, but
// a deterministic local provider. No credentials or live model are involved.
type journalCodexProvider struct {
	mu                sync.Mutex
	turns             map[string]int
	childResultSeen   bool
	journalResultSeen bool
	childRequests     int
}

func (p *journalCodexProvider) forwardExecution(_, _ context.Context, body []byte, headers http.Header, _ string) (*http.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	metadata, _ := decodeCodexTurnMetadata(headers)
	thread := headers.Get(threadIDHeader)
	if thread == "" {
		thread = metadata.ThreadID
	}
	p.turns[thread]++
	turn := p.turns[thread]
	var request map[string]json.RawMessage
	if err := json.Unmarshal(body, &request); err != nil {
		return nil, err
	}
	input := string(request["input"])
	child := metadata.SubagentKind != ""
	var item map[string]any
	call := func(name string, args any) map[string]any {
		return map[string]any{"type": "function_call", "id": fmt.Sprintf("fc_%s_%d", thread, turn), "call_id": fmt.Sprintf("call_%s_%d", thread, turn), "name": name, "namespace": map[bool]string{true: "functions", false: "journal_fixture_agents"}[name == "journal"], "arguments": string(mustMarshalJSON(args)), "status": "completed"}
	}
	if child {
		p.childRequests++
		if turn != 1 {
			return nil, fmt.Errorf("child finish triggered an extra provider request")
		}
		item = call("journal", map[string]any{"op": "finish", "journal": []any{map[string]any{"op": "add", "text": "Native child milestone", "report_now": true}}})
	} else {
		switch {
		case turn == 1:
			item = call("journal", map[string]any{"op": "add", "text": "Native root milestone", "report_now": true})
		case turn == 2:
			item = call("spawn_agent", map[string]any{"message": "Record your milestone and finish.", "task_name": "journal_child", "fork_turns": "none"})
		case strings.Contains(input, "Journal saved: 1 pending, 0 already flushed"):
			p.childResultSeen = true
			p.journalResultSeen = strings.Contains(input, "function_call_output") && strings.Contains(input, `\"id\":\"j1\"`)
			item = call("journal", map[string]any{"op": "finish"})
		case turn < 8:
			item = call("wait_agent", map[string]any{"timeout_ms": 10000})
		default:
			return nil, fmt.Errorf("native parent never received child journal summary")
		}
	}
	response := map[string]any{"id": fmt.Sprintf("resp_%s_%d", thread, turn), "status": "completed", "output": []any{item}, "usage": map[string]any{"input_tokens": 10, "output_tokens": 5, "total_tokens": 15}}
	added := mustMarshalJSON(map[string]any{"type": "response.output_item.added", "output_index": 0, "item": item})
	done := mustMarshalJSON(map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item})
	completed := mustMarshalJSON(map[string]any{"type": "response.completed", "response": response})
	wire := "data: " + string(mustMarshalJSON(map[string]any{"type": "response.created", "response": map[string]any{"id": response["id"], "status": "in_progress", "output": []any{}}})) + "\n\n"
	wire += "data: " + string(added) + "\n\ndata: " + string(done) + "\n\ndata: " + string(completed) + "\n\n"
	result := serverHTTPResponse(wire)
	result.Header.Set("Content-Type", "text/event-stream")
	return result, nil
}

func TestJournalNativeCodexSpawnE2E(t *testing.T) {
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal("native Codex is required for the journal acceptance gate")
	}
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	workspace := t.TempDir()
	provider := &journalCodexProvider{turns: make(map[string]int)}
	proxy := newManagedMekugiProxy(t, newInProcessMekugiTranslator(t.TempDir()))
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	proxy.replayStore = store
	issues := NewCriticalErrors()
	server := httptest.NewServer(responsesHandler(t.Context(), time.Minute, provider, issues, proxy, nil, nil))
	defer server.Close()
	config := `model_providers.journal_fixture={name="journal_fixture",base_url=` + strconv.Quote(server.URL+"/v1") + `,wire_api="responses",requires_openai_auth=false}`
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, codex,
		"-c", config, "-c", `model_provider="journal_fixture"`,
		"-c", `features.multi_agent_v2={enabled=true,tool_namespace="journal_fixture_agents"}`,
		"-c", "tools.update_plan.enabled=false",
		"-c", "include_collaboration_mode_instructions=false",
		"--model", "gpt-6-astra", "--sandbox", "read-only", "--ask-for-approval", "never",
		"exec", "--ignore-user-config", "--skip-git-repo-check", "--json", "--color", "never",
		"-C", workspace, "Exercise the native journal child fixture.")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("native Codex fixture: %v\nstdout: %.8000s\nstderr: %.8000s", err, stdout.String(), stderr.String())
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if !provider.childResultSeen || !provider.journalResultSeen {
		t.Fatalf("native consumer lost journal result or child summary: child=%v journal=%v\nstdout: %.8000s\nstderr: %.8000s", provider.childResultSeen, provider.journalResultSeen, stdout.String(), stderr.String())
	}
	if provider.childRequests != 1 {
		t.Fatalf("child provider requests = %d, want exactly one", provider.childRequests)
	}
	if !strings.Contains(stdout.String(), "Journal update") || !strings.Contains(stdout.String(), "Journal flush ") {
		t.Fatal("native consumer did not display distinct live updates and terminal flushes")
	}
	issues.mu.Lock()
	noticeCount := len(issues.entries)
	issues.mu.Unlock()
	if noticeCount != 0 {
		t.Fatalf("fixture produced %d critical notices", noticeCount)
	}
}
