//go:build journal_e2e

package router

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yusing/mekugi"
)

// This fixture uses the installed Codex consumer and native collaboration, but
// a deterministic local provider. No credentials or live model are involved.
type journalCodexProvider struct {
	store             *mekugiReplayStore
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
	var inputItems []map[string]json.RawMessage
	if err := json.Unmarshal(request["input"], &inputItems); err != nil {
		return nil, err
	}
	for _, inputItem := range inputItems {
		kind := jsonString(inputItem, "type")
		content := inputItem["content"]
		liveText := bytes.Contains(content, []byte("Native child live milestone"))
		if (kind == "message" || kind == "agent_message") && bytes.Contains(content, []byte("Journal update")) ||
			kind == "message" && liveText ||
			kind == "agent_message" && liveText && !bytes.Contains(content, []byte("Message Type: FINAL_ANSWER")) {

			return nil, fmt.Errorf("user-only journal update leaked into provider message input: %s", mustMarshalJSON(inputItem))
		}
	}

	child := metadata.SubagentKind != ""
	var item map[string]any
	call := func(name string, args any) map[string]any {
		return map[string]any{"type": "function_call", "id": fmt.Sprintf("fc_%s_%d", thread, turn), "call_id": fmt.Sprintf("call_%s_%d", thread, turn), "name": name, "namespace": map[bool]string{true: "functions", false: subagentBridgeNamespace}[name == "journal"], "arguments": string(mustMarshalJSON(args)), "status": "completed"}
	}
	if child {
		p.childRequests++
		if turn > 2 {
			return nil, fmt.Errorf("child natural completion triggered an extra provider request")
		}
		if turn == 1 {
			workspace, _ := usableRoutingDirectory(metadata.Directories)
			id, err := p.store.reserveChange(context.Background(), workspace, thread, "native-child-edit")
			if err != nil {
				return nil, err
			}
			err = p.store.put(context.Background(), workspace, map[string]mekugiHistory{"native-child-edit": {
				ChangeID: id, CorrelationID: "native-child-edit", ExecutingThread: thread, ReviewFiles: []mekugi.ReviewFile{mekugi.RenderReviewFile("native-child.txt", "native-child.txt", "before\n", "after\n")},
			}})
			if err != nil {
				return nil, err
			}
		}
		if turn == 1 {
			item = call("journal", map[string]any{"op": "add", "text": "Native child live milestone", "report_now": true})
		} else {
			item = map[string]any{"type": "message", "id": fmt.Sprintf("answer_%s_%d", thread, turn), "role": "assistant",
				"phase": "final_answer", "status": "completed",
				"content": []any{map[string]any{"type": "output_text", "text": "Native child milestone\n\nNative child second finding"}}}
		}
	} else {
		switch {
		case turn == 1:
			item = call("journal", map[string]any{"op": "add", "text": "Native root milestone", "report_now": true})
		case turn == 2:
			item = call("spawn_agent", map[string]any{"message": "Record your milestone, then report your findings.", "task_name": "journal_child", "fork_turns": "none"})
		case strings.Contains(input, "Journal result") && strings.Contains(input, "Native child milestone") && strings.Contains(input, "Native child second finding") && strings.Contains(input, "**Question:**") && strings.Contains(input, "**Answer:**") && strings.Contains(input, "Record your milestone, then report your findings."):
			if !strings.Contains(input, "**Changes:**") || !strings.Contains(input, "amber1") || !strings.Contains(input, `M\t1\t1\tnative-child.txt`) {
				start := strings.LastIndex(input, "**Changes:**")
				if start < 0 {
					start = 0
				}
				return nil, fmt.Errorf("native completion lost child change ranges or numstat: %.500s", input[start:])
			}
			p.childResultSeen = true
			p.journalResultSeen = strings.Contains(input, "function_call_output") && strings.Contains(input, `\"id\":\"amber\"`)
			item = map[string]any{"type": "message", "id": fmt.Sprintf("answer_%s_%d", thread, turn), "role": "assistant",
				"phase": "final_answer", "status": "completed",
				"content": []any{map[string]any{"type": "output_text", "text": "Native root completion after child review."}}}
		case turn < 8:
			item = call("wait_agent", map[string]any{"timeout_ms": 10000})
		default:
			return nil, fmt.Errorf("native parent never received child journal summary")
		}
	}
	response := map[string]any{"id": fmt.Sprintf("resp_%s_%d", thread, turn), "status": "completed", "output": []any{item}, "usage": map[string]any{"input_tokens": 10, "input_tokens_details": map[string]any{"cached_tokens": 0}, "output_tokens": 5, "output_tokens_details": map[string]any{"reasoning_tokens": 0}, "total_tokens": 15}}
	added := mustMarshalJSON(map[string]any{"type": "response.output_item.added", "output_index": 0, "item": item})
	done := mustMarshalJSON(map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item})
	completed := mustMarshalJSON(map[string]any{"type": "response.completed", "response": response})
	wire := "data: " + string(mustMarshalJSON(map[string]any{"type": "response.created", "response": map[string]any{"id": response["id"], "status": "in_progress", "output": []any{}}})) + "\n\n"
	wire += "data: " + string(added) + "\n\n"
	if item["type"] == "function_call" {
		wire += "data: " + string(mustMarshalJSON(map[string]any{
			"type": "response.function_call_arguments.done", "item_id": item["id"],
			"arguments": item["arguments"],
		})) + "\n\n"
	}
	wire += "data: " + string(done) + "\n\ndata: " + string(completed) + "\n\n"

	result := serverHTTPResponse(wire)
	result.Header.Set("Content-Type", "text/event-stream")
	return result, nil
}

func TestJournalNativeCodexSpawnE2E(t *testing.T) {
	runJournalNativeCodexSpawnE2E(t)
}

func runJournalNativeCodexSpawnE2E(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal("native Codex is required for the journal acceptance gate")
	}
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	workspace := t.TempDir()
	provider := &journalCodexProvider{turns: make(map[string]int)}
	proxy := newManagedMekugiProxy(t)
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	provider.store = store
	proxy.replayStore = store
	issues := NewCriticalErrors()
	server := httptest.NewServer(responsesHandler(t.Context(), time.Minute, provider, issues, proxy, nil))
	defer server.Close()
	config := `model_providers.journal_fixture={name="journal_fixture",base_url=` + strconv.Quote(server.URL+"/v1") + `,wire_api="responses",requires_openai_auth=false}`
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, codex,
		"-c", config, "-c", `model_provider="journal_fixture"`,
		"-c", `features.multi_agent_v2={enabled=true,tool_namespace="collaboration"}`,
		"-c", "tools.update_plan.enabled=false",
		"-c", "include_collaboration_mode_instructions=false",
		"--model", "gpt-6-astra", "--sandbox", "danger-full-access", "--ask-for-approval", "never",
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
	childStart, childLiveUpdate := false, false
	rootFlushes := 0
	lastMessage := ""
	for line := range strings.SplitSeq(stdout.String(), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var event struct {
			Type string `json:"type"`
			Item struct {
				Text string `json:"text"`
			} `json:"item"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("invalid native JSON output: %v", err)
		}
		if event.Type != "item.completed" {
			continue
		}
		text := event.Item.Text
		if strings.Contains(text, "Router session usage") {
			t.Fatalf("native completion exposed token metrics as commentary: %s", text)
		}
		if text != "" {
			lastMessage = text
		}
		if strings.HasPrefix(text, "Journal flush `/root`") {
			rootFlushes++
		}
		childLiveUpdate = childLiveUpdate || strings.Contains(text, "Journal update `/root/journal_child`") && strings.Contains(text, "Native child live milestone")
		if strings.Contains(text, " -> ") && (strings.Contains(text, "Completed.") || strings.Contains(text, "Journal update")) {
			t.Fatalf("duplicate completion or journal recipient commentary: %s", text)
		}
		if strings.Contains(text, "Journal flush `/root/journal_child`") {
			t.Fatalf("main repeated the native child result: %s", text)
		}
		childStart = childStart || strings.Contains(text, "[`/root/journal_child`] Started · ")
	}
	if rootFlushes != 1 || !strings.HasPrefix(lastMessage, "Journal flush `/root`") {
		t.Fatalf("native consumer must receive one journal flush as its last message; flushes=%d last=%s", rootFlushes, lastMessage)
	}
	if !childStart {
		t.Fatalf("native consumer did not display the compact child start: %.8000s", stdout.String())
	}
	if provider.childRequests != 2 {
		t.Fatalf("child provider requests = %d, want live update then a natural final answer without another request", provider.childRequests)
	}
	paths := proxy.tokenMetricPaths()
	if len(paths) != 1 {
		t.Fatalf("main completion metric paths = %q", paths)
	}
	markdown, err := os.ReadFile(paths[0])
	if err != nil || !strings.Contains(string(markdown), "| journal_child |") || !strings.Contains(string(markdown), "| Total |") {
		t.Fatalf("main metrics omitted native child usage: %q, %v", markdown, err)
	}
	if !childLiveUpdate || !strings.Contains(stdout.String(), "Journal flush ") {
		t.Fatal("native consumer did not display distinct live updates and terminal flushes")
	}
	issues.mu.Lock()
	noticeCount := len(issues.entries)
	issues.mu.Unlock()
	if noticeCount != 0 {
		t.Fatalf("fixture produced %d critical notices", noticeCount)
	}
}
