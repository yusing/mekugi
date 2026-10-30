//go:build codex_e2e && !windows

package sessioncontrol

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/libraries/commandargs"
)

// The local provider is scripted. Installed Codex owns execution, process-group
// shutdown, history delivery and polling; no external inference is used.
func TestStopProcessNativeCodex(t *testing.T) {
	for _, active := range []bool{false, true} {
		t.Run(fmt.Sprintf("active=%t", active), func(t *testing.T) { testStopProcessNativeCodex(t, active) })
	}
}

func testStopProcessNativeCodex(t *testing.T, active bool) {
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	shell, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	pidPath := filepath.Join(workspace, "pids")
	var calls atomic.Int32
	var processID atomic.Int64
	var noticeSeen, pollSeen, retainedOutput atomic.Bool
	ready, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Input []struct {
				Type   string `json:"type"`
				Role   string `json:"role"`
				Output []struct {
					Text string `json:"text"`
				} `json:"output"`
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"input"`
		}
		if err := json.UnmarshalRead(r.Body, &request); err != nil {
			t.Error(err)
			http.Error(w, "bad input", 400)
			return
		}
		n := calls.Add(1)
		item := map[string]any{"type": "message", "id": fmt.Sprintf("answer-%d", n), "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "Fixture complete.", "annotations": []any{}}}}
		program := ""
		switch n {
		case 1:
			command := "sleep 60 & child=$!; printf '%s %s' \"$$\" \"$child\" > " + commandargs.Quote(pidPath) + "; printf 'started\\n' >> starts; printf 'output before cancellation\\n'; wait \"$child\""
			program = `const result = await tools.exec_command({cmd:` + strconv.Quote(command) + `,shell:` + strconv.Quote(shell) + `,login:false,workdir:` + strconv.Quote(workspace) + `,yield_time_ms:1000}); text(JSON.stringify(result));`
		case 2:
			for _, input := range request.Input {
				if input.Type != "custom_tool_call_output" {
					continue
				}
				for _, output := range input.Output {
					match := regexp.MustCompile(`"session_id":\s*(\d+)`).FindStringSubmatch(output.Text)
					if len(match) == 2 {
						id, _ := strconv.ParseInt(match[1], 10, 64)
						processID.Store(id)
					}
				}
			}
			close(ready)
			if active {
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
			}
		case 3:
			for _, input := range request.Input {
				for _, output := range input.Output {
					if strings.Contains(output.Text, "output before cancellation") {
						retainedOutput.Store(true)
					}
				}
				for _, content := range input.Content {
					if input.Role == "developer" && content.Text == processCancellationNotice(strconv.FormatInt(processID.Load(), 10)) {
						noticeSeen.Store(true)
					}
				}
			}
			program = fmt.Sprintf(`try { text(await tools.write_stdin({session_id:%d,chars:"",yield_time_ms:1000})); } catch (error) { text(String(error)); }`, processID.Load())
		case 4:
			for _, input := range request.Input {
				if input.Type == "custom_tool_call_output" {
					for _, output := range input.Output {
						if strings.Contains(output.Text, "Unknown process id") {
							pollSeen.Store(true)
						}
					}
				}
			}
		default:
			t.Errorf("unexpected model request %d", n)
		}
		if program != "" {
			item = map[string]any{"type": "custom_tool_call", "id": fmt.Sprintf("item-%d", n), "call_id": fmt.Sprintf("call-%d", n), "name": "exec", "status": "completed", "input": program}
		}
		response := map[string]any{"id": fmt.Sprintf("response-%d", n), "status": "completed", "output": []any{item}, "usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range []map[string]any{
			{"type": "response.created", "response": map[string]any{"id": response["id"], "status": "in_progress", "output": []any{}}},
			{"type": "response.output_item.added", "output_index": 0, "item": item},
			{"type": "response.output_item.done", "output_index": 0, "item": item},
			{"type": "response.completed", "response": response},
		} {
			data, err := json.Marshal(event)
			if err != nil {
				t.Error(err)
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", data)
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, codex, "app-server", "-c", `model_providers.fixture={name="fixture",base_url=`+strconv.Quote(server.URL+"/v1")+`,wire_api="responses",requires_openai_auth=false}`, "-c", `model_provider="fixture"`, "-c", `model="gpt-6-astra"`, "-c", "features.plugins=false", "-c", "features.code_mode=true", "-c", "features.code_mode_only=true")
	cmd.Dir = workspace
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "CODEX_HOME", "XDG_CONFIG_HOME", "BASH_ENV", "MEKUGI_EXEC_TRACK", "OPENAI_API_KEY", "CODEX_API_KEY", "CHATGPT_API_KEY", "OPENAI_BASE_URL":
			continue
		}
		cmd.Env = append(cmd.Env, entry)
	}
	cmd.Env = append(cmd.Env, "CODEX_HOME="+t.TempDir(), "XDG_CONFIG_HOME="+t.TempDir(), "NO_PROXY=*")
	host, err := appserver.Start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := host.Shutdown(); err != nil {
			t.Error(err)
		}
	}()
	requests := NewRequests[string](host)
	next := func() appserver.Message {
		t.Helper()
		select {
		case message := <-host.Messages:
			return message
		case <-ctx.Done():
			t.Fatal(ctx.Err())
			return appserver.Message{}
		}
	}
	awaitReply := func(id string) appserver.Message {
		t.Helper()
		for {
			message := next()
			if string(message.ID) == id {
				if message.Error != nil {
					t.Fatalf("host response: %+v", message.Error)
				}
				return message
			}
		}
	}
	send := func(method string, params any) appserver.Message {
		t.Helper()
		id, err := host.Send(method, params, true)
		if err != nil {
			t.Fatal(err)
		}
		return awaitReply(id)
	}
	id, err := host.Initialize()
	if err != nil {
		t.Fatal(err)
	}
	awaitReply(id)
	if _, err := host.Send("initialized", map[string]any{}, false); err != nil {
		t.Fatal(err)
	}
	started := send("thread/start", map[string]any{"cwd": workspace, "approvalPolicy": "never", "sandbox": "danger-full-access"})
	var thread struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(started.Result, &thread); err != nil || thread.Thread.ID == "" {
		t.Fatalf("thread: %s, %v", started.Result, err)
	}
	awaitTurn := func() {
		t.Helper()
		for {
			message := next()
			if message.Method == "turn/completed" {
				if strings.Contains(string(message.Params), `"failed"`) {
					t.Fatalf("turn failed: %s", message.Params)
				}
				return
			}
		}
	}
	send("turn/start", map[string]any{"threadId": thread.Thread.ID, "input": appserver.Input("Start the fixture command and leave it running.")})
	if active {
		select {
		case <-ready:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	} else {
		awaitTurn()
	}
	if processID.Load() == 0 || calls.Load() != 2 {
		t.Fatalf("missing yielded session: id=%d calls=%d", processID.Load(), calls.Load())
	}
	data, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatal(err)
	}
	pids := strings.Fields(string(data))
	if len(pids) != 2 {
		t.Fatalf("missing descendants: %q", data)
	}
	var descendants []*os.Process
	for _, value := range pids {
		pid, err := strconv.Atoi(value)
		if err != nil || pid <= 0 {
			t.Fatalf("invalid PID %q", value)
		}
		process, err := os.FindProcess(pid)
		if err != nil || process.Signal(syscall.Signal(0)) != nil {
			t.Fatalf("descendant not alive: %s %v", value, err)
		}
		descendants = append(descendants, process)
		defer process.Release()
		defer process.Kill()
	}
	if err := requests.StopProcess(thread.Thread.ID, strconv.FormatInt(processID.Load(), 10), "detached-view"); err != nil {
		t.Fatal(err)
	}
	for requests.Pending() {
		message := next()
		if request, ok := requests.Take(string(message.ID)); ok {
			if handled, warning := requests.ProcessResponse(request, message); !handled || warning != "" {
				t.Fatalf("stop response: %t %s", handled, warning)
			}
		}
	}
	// A host read is a barrier after both acknowledgements. Cancellation only
	// records history: no automatic turn, provider call or command replay.
	send("thread/read", map[string]any{"threadId": thread.Thread.ID, "includeTurns": true})
	if calls.Load() != 2 {
		t.Fatal("cancellation restarted inference")
	}
	for _, process := range descendants {
		for {
			err := process.Signal(syscall.Signal(0))
			if errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH) {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatalf("descendant %d survived stop", process.Pid)
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	if active {
		close(release)
	} else {
		send("turn/start", map[string]any{"threadId": thread.Thread.ID, "input": appserver.Input("Inspect the retained cancellation and poll the stopped session once.")})
	}
	awaitTurn()
	if !noticeSeen.Load() || !pollSeen.Load() || !retainedOutput.Load() || calls.Load() != 4 {
		t.Fatalf("notice=%t stopped polling=%t retained output=%t requests=%d", noticeSeen.Load(), pollSeen.Load(), retainedOutput.Load(), calls.Load())
	}
	data, err = os.ReadFile(filepath.Join(workspace, "starts"))
	if err != nil || string(data) != "started\n" {
		t.Fatalf("command restarted: %q %v", data, err)
	}
}
