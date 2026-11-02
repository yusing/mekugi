//go:build codex_e2e

package launch

import (
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/commandargs"
)

// The installed host persists real paginated turns against a deterministic local
// provider. No model or personal session is used.
func TestAppServerRewindBoundaryNativeCodex(t *testing.T) {
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		item := map[string]any{"type": "message", "id": "answer", "role": "assistant", "status": "completed", "phase": "final_answer", "content": []any{map[string]any{"type": "output_text", "text": "Fixture answer", "annotations": []any{}}}}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, routerFaultSSE(
			map[string]any{"type": "response.created", "response": map[string]any{"id": "fixture", "status": "in_progress", "output": []any{}}},
			map[string]any{"type": "response.output_item.added", "output_index": 0, "item": item},
			map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item},
			map[string]any{"type": "response.completed", "response": map[string]any{"id": "fixture", "status": "completed", "output": []any{item}}},
		))
	}))
	defer server.Close()
	environment, workspace := routerFaultCodexEnvironment(t), t.TempDir()
	start := func() *appserver.Client {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), codex, "app-server", "-c", "features.plugins=false", "-c", `model_providers.rewind_fixture={name="rewind_fixture",base_url=`+strconv.Quote(server.URL+"/v1")+`,wire_api="responses",requires_openai_auth=false}`, "-c", `model_provider="rewind_fixture"`, "-c", `model="gpt-6-astra"`)
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
		if _, err = client.Send("initialized", map[string]any{}, false); err != nil {
			t.Fatal(err)
		}
		return client
	}
	client := start()
	rpc := func(method string, params any) appserver.Message {
		t.Helper()
		id, err := client.Send(method, params, true)
		if err != nil {
			t.Fatal(err)
		}
		return awaitHistoryRPC(t, client, id)
	}
	response := rpc("thread/start", map[string]any{"cwd": workspace, "historyMode": "paginated", "approvalPolicy": "never", "sandbox": "danger-full-access"})
	var started struct {
		Thread binding.ThreadInfo `json:"thread"`
	}
	if err := json.Unmarshal(response.Result, &started); err != nil {
		t.Fatal(err)
	}
	thread := started.Thread.ID
	var turns []string
	for i := range 3 {
		response = rpc("turn/start", map[string]any{"threadId": thread, "input": []any{map[string]any{"type": "text", "text": fmt.Sprintf("User request %d", i)}}})
		var started struct {
			Turn binding.HistoryTurn `json:"turn"`
		}
		if err := json.Unmarshal(response.Result, &started); err != nil {
			t.Fatal(err)
		}
		turns = append(turns, started.Turn.ID)
		deadline := time.NewTimer(30 * time.Second)
		for {
			select {
			case m := <-client.Messages:
				if m.Method != "turn/completed" {
					continue
				}
				var completed struct {
					Turn binding.HistoryTurn `json:"turn"`
				}
				if err := json.Unmarshal(m.Params, &completed); err != nil {
					t.Fatal(err)
				}
				if completed.Turn.Status != "completed" {
					t.Fatalf("fixture turn failed: %s", m.Params)
				}
			case <-deadline.C:
				t.Fatal("fixture turn timed out")
			}
			break
		}
		deadline.Stop()
	}
	u, wire := newResumeSessionTestUI(t)
	u.startSession(thread, workspace)
	u.Thread, u.Startup.Thread = thread, thread
	u.settings.Model, u.settings.Effort = "gpt-6-astra", "high"
	appServerTestMessage(t, u, fmt.Sprintf(`{"method":"item/completed","params":{"threadId":%q,"turnId":%q,"item":{"type":"userMessage","id":"seed","content":[{"type":"text","text":"User request 0"}]}}}`, thread, turns[0]))
	relay := func(method string) appserver.Message {
		t.Helper()
		requests := appServerDrainRequests[struct {
			ID     int            `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}](t, wire)
		if len(requests) != 1 || requests[0].Method != method {
			t.Fatalf("host requests: %+v, want %s", requests, method)
		}
		result := rpc(method, requests[0].Params)
		appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":%s}`, requests[0].ID, result.Result))
		return result
	}
	appServerTestKeys(t, u, "/rewind\r")
	relay("thread/read")
	relay("thread/turns/list")
	if len(u.picker.Choices) != 3 {
		t.Fatalf("user turns missing: %+v", u.picker)
	}
	appServerTestKeys(t, u, "\x1b[B\r\r")
	reverted := relay("thread/revert")
	var native struct {
		Thread      binding.ThreadInfo `json:"thread"`
		TurnsCursor *string            `json:"turnsBackwardsCursor"`
	}
	if err := json.Unmarshal(reverted.Result, &native); err != nil {
		t.Fatal(err)
	}
	if native.Thread.ID != thread || len(native.Thread.Turns) != 0 || native.TurnsCursor == nil {
		t.Fatalf("native revert response: %s", reverted.Result)
	}
	relay("thread/turns/list")
	resumeTestSettle(t, u, wire)
	if u.Thread != thread || u.picker.Kind != "" || u.Replacement.Pending() || !u.HasUserTurn {
		t.Fatal("same-thread rewind did not restore retained conversation")
	}
	rows, _ := u.testMainFrame(100, 30, 0)
	frame := strings.Join(rows, "\n")
	if !strings.Contains(frame, "User request 0") || strings.Contains(frame, "User request 1") || strings.Contains(frame, "User request 2") {
		t.Fatalf("retained transcript: %s", frame)
	}
	if err := client.Shutdown(); err != nil {
		t.Fatal(err)
	}
	client = start()
	response = rpc("thread/resume", map[string]any{"threadId": thread})
	var resumed struct {
		Thread binding.ThreadInfo `json:"thread"`
	}
	if err := json.Unmarshal(response.Result, &resumed); err != nil {
		t.Fatal(err)
	}
	if resumed.Thread.ID != thread {
		t.Fatal("fresh resume changed identity")
	}
	response = rpc("thread/turns/list", historyPageParams(thread, ""))
	var page struct {
		Data []binding.HistoryTurn `json:"data"`
	}
	if err := json.Unmarshal(response.Result, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Data) != 1 || page.Data[0].ID != turns[0] {
		t.Fatalf("fresh resume lost rewind boundary: %s", response.Result)
	}
	if runtime.GOOS == "windows" {
		return
	} // Signal(0) cannot observe process continuity there.
	// Same native identity does not imply the same execution manager. Revert
	// stops the caller's live shell while an independent runtime keeps running.
	response = rpc("thread/start", map[string]any{"cwd": workspace, "historyMode": "paginated", "approvalPolicy": "never", "sandbox": "danger-full-access"})
	if err := json.Unmarshal(response.Result, &started); err != nil {
		t.Fatal(err)
	}
	child := started.Thread.ID
	processes := make(map[string]*os.Process)
	for _, target := range []string{thread, child} {
		path := filepath.Join(workspace, target+".pid")
		rpc("thread/shellCommand", map[string]any{"threadId": target, "command": "printf '%s' $$ >" + commandargs.Quote(path) + "; exec sleep 60"})
		var pid int
		serverCLIAwait(t, func() bool {
			text, err := os.ReadFile(path)
			if err != nil {
				return false
			}
			pid, err = strconv.Atoi(string(text))
			return err == nil
		})
		process, err := os.FindProcess(pid)
		if err != nil {
			t.Fatal(err)
		}
		defer process.Release()
		if err := process.Signal(syscall.Signal(0)); err != nil {
			t.Fatal("shell did not start:", err)
		}
		processes[target] = process
	}
	rpc("thread/revert", map[string]any{"threadId": thread, "beforeTurnId": turns[0]})
	serverCLIAwait(t, func() bool { return processes[thread].Signal(syscall.Signal(0)) != nil })
	if err := processes[child].Signal(syscall.Signal(0)); err != nil {
		t.Fatal("revert stopped the independent runtime:", err)
	}
}
