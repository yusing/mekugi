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
	childLiveVisible  chan struct{}
	hostFinish        bool
	rootFinishSent    bool
	childFinishSent   bool
	hostOutputs       []string
}

func (p *journalCodexProvider) forwardExecution(ctx, _ context.Context, body []byte, headers http.Header, _ string) (*http.Response, error) {
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
	var advertised []map[string]json.RawMessage
	if rawTools := request["tools"]; len(rawTools) != 0 {
		if err := json.Unmarshal(rawTools, &advertised); err != nil {
			return nil, err
		}
	}
	var checkTools func([]map[string]json.RawMessage) error
	checkTools = func(catalog []map[string]json.RawMessage) error {
		for _, tool := range catalog {
			if jsonString(tool, "name") == "journal" {
				return fmt.Errorf("standalone journal tool exposed to installed Codex")
			}
			if nested := tool["tools"]; len(nested) != 0 {
				var children []map[string]json.RawMessage
				if err := json.Unmarshal(nested, &children); err != nil {
					return err
				}
				if err := checkTools(children); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := checkTools(advertised); err != nil {
		return nil, err
	}
	input := string(request["input"])
	var inputItems []map[string]json.RawMessage
	if err := json.Unmarshal(request["input"], &inputItems); err != nil {
		return nil, err
	}
	for _, inputItem := range inputItems {
		kind := jsonString(inputItem, "type")
		if (kind == "custom_tool_call_output" || kind == "function_call_output") && len(p.hostOutputs) < 12 {
			p.hostOutputs = append(p.hostOutputs, string(mustMarshalJSON(inputItem)))
		}
		content := inputItem["content"]
		liveText := bytes.Contains(content, []byte("Native child live milestone"))
		if (kind == "message" || kind == "agent_message") && (bytes.Contains(content, []byte("Journal update")) || bytes.Contains(content, []byte(`Journal\n`))) ||
			kind == "message" && liveText ||
			kind == "agent_message" && liveText && !bytes.Contains(content, []byte("Message Type: FINAL_ANSWER")) {

			return nil, fmt.Errorf("user-only journal update leaked into provider message input: %s", mustMarshalJSON(inputItem))
		}
	}

	child := metadata.SubagentKind != ""
	var item map[string]any
	call := func(name string, args any) map[string]any {
		return map[string]any{"type": "function_call", "id": fmt.Sprintf("fc_%s_%d", thread, turn), "call_id": fmt.Sprintf("call_%s_%d", thread, turn), "name": name, "namespace": subagentBridgeNamespace, "arguments": string(mustMarshalJSON(args)), "status": "completed"}
	}
	journalCall := func(title string) map[string]any {
		return map[string]any{"type": "custom_tool_call", "id": fmt.Sprintf("fc_%s_%d", thread, turn), "call_id": fmt.Sprintf("call_%s_%d", thread, turn), "name": "exec", "status": "completed", "input": `await journal({op:"add",title:` + string(mustMarshalJSON(title)) + `});`}
	}
	finishCall := func(titles ...string) map[string]any {
		mutations := make([]any, 0, len(titles)+1)
		for _, title := range titles {
			mutations = append(mutations, map[string]any{"op": "add", "title": title})
		}
		mutations = append(mutations, map[string]any{"op": "finish"})
		return map[string]any{"type": "custom_tool_call", "id": fmt.Sprintf("fc_%s_%d", thread, turn), "call_id": fmt.Sprintf("call_%s_%d", thread, turn), "name": "exec", "status": "completed", "input": `const result = await tools.exec_command({cmd:"printf journal-host-finish"}); if (result.exit_code !== 0) throw new Error("fixture host failed"); await journal(` + string(mustMarshalJSON(mutations)) + `);`}
	}
	if child {
		p.childRequests++
		if turn > 2 {
			return nil, fmt.Errorf("child natural completion triggered an extra provider request")
		}
		if turn == 1 {
			workspace, _ := usableRoutingDirectory(metadata.Directories)
			// Fixture writes must use the child's durable handle namespace,
			// just like captures made by the prepared response transform.
			ctx, release, err := p.store.beginSession(ctx, thread, thread)
			if err != nil {
				return nil, err
			}
			defer release()
			id, err := p.store.reserveChange(ctx, workspace, thread, "native-child-edit")
			if err != nil {
				return nil, err
			}
			err = p.store.put(ctx, workspace, map[string]mekugiHistory{"native-child-edit": {
				ChangeID: id, CorrelationID: "native-child-edit", ExecutingThread: thread, ReviewFiles: []mekugi.ReviewFile{mekugi.RenderReviewFile("native-child.txt", "native-child.txt", "before\n", "after\n")},
			}})
			if err != nil {
				return nil, err
			}
		}
		if turn == 1 {
			item = journalCall("Native child live milestone")
		} else {
			if p.childLiveVisible != nil {
				// Keep the child working until the actual root JSON consumer
				// observes its live milestone. A terminal-only replay cannot pass.
				p.mu.Unlock()
				select {
				case <-p.childLiveVisible:
				case <-ctx.Done():
					p.mu.Lock()
					return nil, fmt.Errorf("child live milestone never reached root before child completion: %w", ctx.Err())
				}
				p.mu.Lock()
			}
			if p.hostFinish {
				p.childFinishSent = true
				item = finishCall("Native child milestone", "Native child second finding")
			} else {
				item = map[string]any{"type": "message", "id": fmt.Sprintf("answer_%s_%d", thread, turn), "role": "assistant",
					"phase": "final_answer", "status": "completed",
					"content": []any{map[string]any{"type": "output_text", "text": "Native child milestone\n\nNative child second finding"}}}
			}
		}
	} else {
		if p.rootFinishSent {
			return nil, fmt.Errorf("root host completion triggered an extra provider request")
		}
		switch {
		case turn == 1:
			item = journalCall("Native root milestone")
		case turn == 2:
			item = call("spawn_agent", map[string]any{"message": "Record your milestone, then report your findings.", "task_name": "journal_child", "fork_turns": "none"})
		case strings.Contains(input, "Native child milestone") && strings.Contains(input, "Native child second finding") && strings.Contains(input, "Record your milestone, then report your findings."):
			if !strings.Contains(input, "**Changes:**") || !strings.Contains(input, "amber1") || !strings.Contains(input, `M\t1\t1\tnative-child.txt`) {
				start := strings.LastIndex(input, "**Changes:**")
				if start < 0 {
					start = 0
				}
				return nil, fmt.Errorf("native completion lost child change ranges or numstat: %.500s", input[start:])
			}
			p.childResultSeen = true
			p.journalResultSeen = strings.Contains(input, "custom_tool_call_output") && strings.Contains(input, "Native root milestone")
			if p.hostFinish {
				p.rootFinishSent = true
				item = finishCall("Native root completion after child review.")
			} else {
				item = map[string]any{"type": "message", "id": fmt.Sprintf("answer_%s_%d", thread, turn), "role": "assistant",
					"phase": "final_answer", "status": "completed",
					"content": []any{map[string]any{"type": "output_text", "text": "Native root completion after child review."}}}
			}
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
	runJournalNativeCodexSpawnE2E(t, false)
}

func TestJournalHostFinishNativeCodexSpawnE2E(t *testing.T) {
	runJournalNativeCodexSpawnE2E(t, true)
}

func runJournalNativeCodexSpawnE2E(t *testing.T, hostFinish bool) {
	t.Setenv("TMPDIR", t.TempDir())
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal("native Codex is required for the journal acceptance gate")
	}
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	workspace := t.TempDir()
	provider := &journalCodexProvider{turns: make(map[string]int), childLiveVisible: make(chan struct{}), hostFinish: hostFinish}
	traceRoot := t.TempDir()
	t.Setenv("CODEX_ROLLOUT_TRACE_ROOT", traceRoot)
	proxy := newManagedMekugiProxy(t)
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	provider.store = store
	proxy.replayStore = store
	proxy.nativeTrace = &nativeToolTrace{directory: traceRoot}
	publisher := httptest.NewServer(http.HandlerFunc(proxy.commentary.serveHTTP))
	defer publisher.Close()
	proxy.commentaryEndpoint = publisher.URL + commentaryPublisherPath
	issues := NewCriticalErrors()
	server := httptest.NewServer(responsesHandler(t.Context(), time.Minute, provider, issues, proxy))
	defer server.Close()
	config := `model_providers.journal_fixture={name="journal_fixture",base_url=` + strconv.Quote(server.URL+"/v1") + `,wire_api="responses",requires_openai_auth=false}`
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, codex,
		"-c", config, "-c", `model_provider="journal_fixture"`,
		"-c", "features.plugins=false",
		"-c", "features.code_mode=true", "-c", "features.code_mode_host=true",
		"-c", `features.multi_agent_v2={enabled=true,tool_namespace="collaboration"}`,
		"-c", "tools.update_plan.enabled=false",
		"-c", "include_collaboration_mode_instructions=false",
		"--model", "gpt-6-astra", "--sandbox", "danger-full-access", "--ask-for-approval", "never",
		"exec", "--ignore-user-config", "--skip-git-repo-check", "--json", "--color", "never",
		"-C", workspace, "Exercise the native journal child fixture.")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &journalRootLiveConsumer{output: &stdout, visible: provider.childLiveVisible}, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("native Codex fixture: %v\nstdout: %.8000s\nstderr: %.8000s\noutputs: %q", err, stdout.String(), stderr.String(), provider.hostOutputs)
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if !provider.childResultSeen || !provider.journalResultSeen {
		t.Fatalf("native consumer lost journal result or child summary: child=%v journal=%v\nstdout: %.8000s\nstderr: %.8000s", provider.childResultSeen, provider.journalResultSeen, stdout.String(), stderr.String())
	}
	childLiveUpdate := false
	rootFlushes := 0
	rootFinals := 0
	lastMessage := ""
	var messages []string
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
			messages = append(messages, text)
		}
		if strings.HasPrefix(text, "Journal\n\n**This turn**") {
			rootFlushes++
		}
		if text == "Native root completion after child review." {
			rootFinals++
			if rootFlushes != 1 {
				t.Fatalf("ordinary final must follow one journal flush; flushes=%d messages=%q", rootFlushes, messages)
			}
		}
		childLiveUpdate = childLiveUpdate || (strings.HasPrefix(text, "Journal\n") || strings.HasPrefix(text, "Journal update `/root/journal_child`")) && strings.Contains(text, "Native child live milestone")
		if strings.Contains(text, " -> ") && (strings.Contains(text, "Completed.") || strings.Contains(text, "Journal update") || strings.HasPrefix(text, "Journal\n")) {
			t.Fatalf("duplicate completion or journal recipient commentary: %s", text)
		}
		if strings.Contains(text, "Journal flush `/root/journal_child`") {
			t.Fatalf("main repeated the native child result: %s", text)
		}
	}
	if hostFinish {
		if !provider.rootFinishSent || !provider.childFinishSent || rootFlushes != 1 || rootFinals != 0 || !strings.Contains(lastMessage, "Native root completion after child review.") {
			t.Fatalf("host completion did not end root and child with journal reports: root=%v child=%v flushes=%d finals=%d messages=%q", provider.rootFinishSent, provider.childFinishSent, rootFlushes, rootFinals, messages)
		}
		for _, message := range messages {
			if strings.TrimSpace(message) == "Done." {
				t.Fatalf("host completion added a Done-only final: %q", messages)
			}
		}
	} else if rootFlushes != 1 || rootFinals != 1 || lastMessage != "Native root completion after child review." {
		t.Fatalf("native consumer must receive one journal flush before one ordinary final, with nothing after it; flushes=%d finals=%d last=%s", rootFlushes, rootFinals, lastMessage)
	}
	if provider.childRequests != 2 {
		t.Fatalf("child provider requests = %d, want live update then a natural final answer without another request", provider.childRequests)
	}
	if !childLiveUpdate || rootFlushes != 1 {
		t.Fatalf("native consumer did not display distinct live updates and terminal flushes; messages=%q", messages)
	}
	issues.mu.Lock()
	noticeCount := len(issues.entries)
	issues.mu.Unlock()
	if noticeCount != 0 {
		t.Fatalf("fixture produced %d critical notices", noticeCount)
	}
}

// Inspect the consumer's completed JSONL messages while Codex is running, not
// merely after the terminal flush. stdout has one writer owned by os/exec.
type journalRootLiveConsumer struct {
	output  *bytes.Buffer
	pending []byte
	visible chan struct{}
	once    sync.Once
}

func (c *journalRootLiveConsumer) Write(data []byte) (int, error) {
	c.output.Write(data)
	c.pending = append(c.pending, data...)
	for {
		line, rest, found := bytes.Cut(c.pending, []byte("\n"))
		if !found {
			break
		}
		c.pending = rest
		var event struct {
			Type string `json:"type"`
			Item struct {
				Text string `json:"text"`
			} `json:"item"`
		}
		if json.Unmarshal(line, &event) == nil && event.Type == "item.completed" &&
			(strings.HasPrefix(event.Item.Text, "Journal\n") || strings.HasPrefix(event.Item.Text, "Journal update `/root/journal_child`")) &&
			strings.Contains(event.Item.Text, "Native child live milestone") {
			c.once.Do(func() { close(c.visible) })
		}
	}
	return len(data), nil
}
