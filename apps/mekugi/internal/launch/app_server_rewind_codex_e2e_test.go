//go:build codex_e2e

package launch

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/backends/codex/binding"
)

// Exercise the real exclusive fork boundary without inference or personal history.
func TestAppServerRewindBoundaryNativeCodex(t *testing.T) {
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	environment := routerFaultCodexEnvironment(t)
	var fixtureRoot string
	for _, entry := range environment {
		if value, ok := strings.CutPrefix(entry, "CODEX_HOME="); ok {
			fixtureRoot = value
		}
	}
	workspace := t.TempDir()
	const thread = "019f9b56-5a00-7000-8000-000000000090"
	const stamp = "2026-09-30T00:00:00Z"
	path := filepath.Join(fixtureRoot, "sessions", "2026", "09", "30", "rollout-2026-09-30T00-00-00-"+thread+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	encoder := jsontext.NewEncoder(file)
	write := func(kind string, payload any) {
		t.Helper()
		if err := json.MarshalEncode(encoder, map[string]any{"timestamp": stamp, "type": kind, "payload": payload}); err != nil {
			t.Fatal(err)
		}
	}
	write("session_meta", map[string]any{"id": thread, "session_id": thread, "timestamp": stamp, "cwd": workspace, "originator": "codex", "cli_version": "0.163.0", "source": "cli", "model_provider": "openai", "history_mode": "legacy"})
	for i := range 3 {
		turn := fmt.Sprintf("turn-%d", i)
		text := fmt.Sprintf("User request %d", i)
		write("event_msg", map[string]any{"type": "task_started", "turn_id": turn, "model_context_window": 100000, "collaboration_mode_kind": "default"})
		write("response_item", map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": text}}})
		write("event_msg", map[string]any{"type": "user_message", "message": text, "kind": "plain", "local_images": []any{}, "text_elements": []any{}})
		write("response_item", map[string]any{"type": "message", "role": "assistant", "phase": "final_answer", "content": []any{map[string]any{"type": "output_text", "text": "Answer " + text}}})
		write("event_msg", map[string]any{"type": "task_complete", "turn_id": turn, "last_agent_message": "Answer " + text})
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	start := func() *appserver.Client {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), codex, "app-server", "-c", "features.plugins=false")
		cmd.Env, cmd.Dir = environment, workspace
		client, err := appserver.Start(cmd)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(client.Close)
		id, err := client.Initialize()
		if err != nil {
			t.Fatal(err)
		}
		awaitHistoryRPC(t, client, id)
		if _, err := client.Send("initialized", map[string]any{}, false); err != nil {
			t.Fatal(err)
		}
		return client
	}
	client := start()
	u, wire := newResumeSessionTestUI(t)
	u.startSession(thread, workspace)
	u.Thread, u.Startup.Thread = thread, thread
	u.model, u.reasoningEffort = "gpt-6-astra", "high"
	relay := func(method string) binding.ThreadInfo {
		t.Helper()
		requests := appServerDrainRequests[struct {
			ID     int            `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}](t, wire)
		if len(requests) != 1 || requests[0].Method != method {
			t.Fatalf("unexpected host requests: %+v", requests)
		}
		id, err := client.Send(method, requests[0].Params, true)
		if err != nil {
			t.Fatal(err)
		}
		response := awaitHistoryRPC(t, client, id)
		var result struct {
			Thread binding.ThreadInfo `json:"thread"`
		}
		if err := json.Unmarshal(response.Result, &result); err != nil {
			t.Fatal(err)
		}
		appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":%s}`, requests[0].ID, response.Result))
		return result.Thread
	}
	appServerTestKeys(t, u, "/rewind\r")
	if original := relay("thread/read"); len(original.Turns) != 3 || len(u.picker.Choices) != 3 {
		t.Fatalf("fixture user turns unavailable: %+v", original)
	}
	appServerTestKeys(t, u, "\x1b[B\r")
	branch := relay("thread/fork")
	if branch.ID == thread || len(branch.Turns) != 1 || branch.Turns[0].ID != "turn-0" {
		t.Fatalf("host did not exclude selected and later turns: %+v", branch)
	}
	resumeTestSettle(t, u, wire)
	appServerTestKeys(t, u, "\r") // Keep the original.
	if u.Thread != branch.ID || wire.Len() != 0 {
		t.Fatal("switch did not retain both threads")
	}
	if err := client.Shutdown(); err != nil {
		t.Fatal(err)
	}
	client = start()
	for target, count := range map[string]int{thread: 3, branch.ID: 1} {
		id, err := client.Send("thread/resume", map[string]any{"threadId": target}, true)
		if err != nil {
			t.Fatal(err)
		}
		response := awaitHistoryRPC(t, client, id)
		info, err := binding.DecodeHistoryThread(response.Result, target)
		if err != nil || len(info.Turns) != count {
			t.Fatalf("fresh resume lost saved boundary: %+v, %v", info, err)
		}
	}
}
