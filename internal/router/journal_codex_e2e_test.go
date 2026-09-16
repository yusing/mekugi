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
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/shellruntime"
)

// The registry pins the test executable. This tagged fixture dispatches only
// its own shell worker children through the real worker entry point.
func init() {
	if os.Getenv("MEKUGI_JOURNAL_E2E_WORKER") == "1" && filepath.Base(os.Args[0]) == "shell" {
		if handled, code := RunToolPluginWorker(context.Background(), os.Args[0], os.Args[1:], os.Stdin, os.Stdout, os.Stderr); handled {
			os.Exit(code)
		}
	}
}

// This fixture uses the installed Codex consumer and native collaboration, but
// a deterministic local provider. No credentials or live model are involved.
type journalCodexProvider struct {
	shellFinish       bool
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
		return map[string]any{"type": "function_call", "id": fmt.Sprintf("fc_%s_%d", thread, turn), "call_id": fmt.Sprintf("call_%s_%d", thread, turn), "name": name, "namespace": map[bool]string{true: "functions", false: subagentBridgeNamespace}[name == "journal"], "arguments": string(mustMarshalJSON(args)), "status": "completed"}
	}
	if child {
		p.childRequests++
		if turn != 1 {
			return nil, fmt.Errorf("child finish triggered an extra provider request")
		}
		item = call("journal", map[string]any{"op": "finish", "journal": []any{
			map[string]any{"op": "add", "text": "Native child milestone", "answer": true},
			map[string]any{"op": "add", "text": "Native child second finding", "answer": true},
		}})
	} else {
		switch {
		case turn == 1:
			item = call("journal", map[string]any{"op": "add", "text": "Native root milestone", "report_now": true})
		case turn == 2:
			item = call("spawn_agent", map[string]any{"message": "Record your milestone and finish.", "task_name": "journal_child", "fork_turns": "none"})
		case strings.Contains(input, "Journal result") && strings.Contains(input, "Native child milestone") && strings.Contains(input, "Native child second finding") && strings.Contains(input, "**Question:**") && strings.Contains(input, "**Answers:**") && strings.Contains(input, "Record your milestone and finish."):
			p.childResultSeen = true
			p.journalResultSeen = strings.Contains(input, "function_call_output") && strings.Contains(input, `\"id\":\"amber\"`)
			item = call("journal", map[string]any{"op": "finish"})
		case turn < 8:
			item = call("wait_agent", map[string]any{"timeout_ms": 10000})
		default:
			return nil, fmt.Errorf("native parent never received child journal summary")
		}
	}
	if p.shellFinish && item["name"] == "journal" {
		var args struct {
			Op      string          `json:"op"`
			Journal json.RawMessage `json:"journal"`
		}
		if err := json.Unmarshal([]byte(item["arguments"].(string)), &args); err != nil {
			return nil, err
		}
		if args.Op == "finish" {
			script := "printf 'SHELL_JOURNAL_HOST_OK\\n'\njournal finish"
			if child {
				script = "#!batch=NEXT_PROGRAM\nprintf 'SHELL_JOURNAL_FIRST_OK\\n'\nNEXT_PROGRAM\n" + script
			}
			if len(args.Journal) != 0 {
				script += " " + shellQuoteArgument(string(args.Journal))
			}
			item = map[string]any{
				"type": "custom_tool_call", "id": item["id"], "call_id": item["call_id"],
				"name": "shell", "namespace": "functions", "input": script, "status": "completed",
			}
		}
	}
	response := map[string]any{"id": fmt.Sprintf("resp_%s_%d", thread, turn), "status": "completed", "output": []any{item}, "usage": map[string]any{"input_tokens": 10, "input_tokens_details": map[string]any{"cached_tokens": 0}, "output_tokens": 5, "output_tokens_details": map[string]any{"reasoning_tokens": 0}, "total_tokens": 15}}
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
	runJournalNativeCodexSpawnE2E(t, false)
}

func TestShellJournalNativeCodexSpawnE2E(t *testing.T) {
	runJournalNativeCodexSpawnE2E(t, true)
}

func runJournalNativeCodexSpawnE2E(t *testing.T, shellFinish bool) {
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal("native Codex is required for the journal acceptance gate")
	}
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	workspace := t.TempDir()
	provider := &journalCodexProvider{turns: make(map[string]int), shellFinish: shellFinish}
	proxy := newManagedMekugiProxy(t, newInProcessMekugiTranslator(t.TempDir()))
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	proxy.replayStore = store
	issues := NewCriticalErrors()
	server := httptest.NewServer(responsesHandler(t.Context(), time.Minute, provider, issues, proxy, nil, nil))
	defer server.Close()
	if shellFinish {
		proxy.commentaryEndpoint = server.URL + commentaryPublisherPath
		// The same listener serves the authenticated runtime publisher.
		server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == commentaryPublisherPath {
				proxy.commentary.serveHTTP(w, r)
				return
			}
			responsesHandler(t.Context(), time.Minute, provider, issues, proxy, nil, nil)(w, r)
		})
		helperDirectory := t.TempDir()
		build := exec.CommandContext(t.Context(), "go", "build", "-o", filepath.Join(helperDirectory, "shell"), "../../cmd/shell")
		if output, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build shell helper: %v\n%s", err, output)
		}
		t.Setenv("PATH", helperDirectory+string(os.PathListSeparator)+os.Getenv("PATH"))
		t.Setenv(shellruntime.RuntimeDirectoryEnvironment, proxy.shellDirectory)
		t.Setenv("MEKUGI_JOURNAL_E2E_WORKER", "1")
	}
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
	groupedResult := false
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
		if strings.Contains(text, "Journal result") && strings.Contains(text, "Native child milestone") {
			if strings.Count(text, "Record your milestone and finish.") != 1 ||
				strings.Count(text, "**Answers:**") != 1 ||
				!strings.Contains(text, "Native child second finding") {
				t.Fatalf("native consumer did not receive one grouped question and answers: %s", text)
			}
			groupedResult = true
		}
	}
	if !groupedResult {
		t.Fatalf("native consumer did not display the grouped child result: %.8000s", stdout.String())
	}
	if provider.childRequests != 1 {
		t.Fatalf("child provider requests = %d, want exactly one", provider.childRequests)
	}
	if shellFinish && (!strings.Contains(stdout.String(), "SHELL_JOURNAL_HOST_OK") || !strings.Contains(stdout.String(), `"exit_code":0`)) {
		t.Fatalf("native shell execution was not observed: %.8000s", stdout.String())
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
