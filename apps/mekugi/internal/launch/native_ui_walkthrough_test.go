package launch

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/backends/codex/binding"
)

func TestNativeUIWalkthrough(t *testing.T) {
	binary := os.Getenv("MEKUGI_PREVIEW_BINARY")
	if binary == "" {
		t.Skip("use make preview-native-ui")
	}
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("the preview needs an interactive terminal: %v", err)
	}
	defer tty.Close()
	if _, err := exec.LookPath("mekugi-app-server"); err != nil {
		t.Fatal("native runtime not found; run make build-native first")
	}

	// Keep the control socket path short, even when the test name or workspace is long.
	root, err := os.MkdirTemp("/tmp", "mekugi-preview-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	// The Saved Diff deliberately omits files under /tmp.
	fixture := savedDiffWorkspace(t)
	workspace := filepath.Join(fixture, "project")
	codexHome := filepath.Join(fixture, "codex")
	t.Setenv("HOME", root)
	for key, dir := range map[string]string{
		"CODEX_HOME": codexHome, "XDG_CONFIG_HOME": filepath.Join(root, "config"),
		"XDG_CACHE_HOME": filepath.Join(root, "cache"), "XDG_STATE_HOME": filepath.Join(root, "state"),
		"XDG_DATA_HOME": filepath.Join(root, "data"),
	} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv(key, dir)
	}
	for path, content := range nativePreviewFiles {
		path = filepath.Join(workspace, path)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	git := exec.Command("git", "init", "--quiet", "--template=")
	git.Dir = workspace
	git.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	if output, err := git.CombinedOutput(); err != nil {
		t.Fatalf("initialize preview workspace: %v\n%s", err, output)
	}

	routes := http.NewServeMux()
	routes.HandleFunc("POST /v1/responses", new(walkthroughProvider).serve)
	provider := httptest.NewServer(routes)
	defer provider.Close()
	profile := `model = "gpt-6-luna"
model_provider = "preview"
approval_policy = "never"
sandbox_mode = "workspace-write"
web_search = "disabled"
[features]
plugins = false
multi_agent_v2 = true
default_mode_request_user_input = true
[model_providers.preview]
name = "Local preview"
base_url = ` + strconv.Quote(provider.URL+"/v1") + `
wire_api = "responses"
requires_openai_auth = false
supports_websockets = false
`
	if err := os.WriteFile(filepath.Join(codexHome, "preview.config.toml"), []byte(profile), 0o600); err != nil {
		t.Fatal(err)
	}

	socket, err := sessionapi.ControlSocketPath()
	if err != nil {
		t.Fatal(err)
	}
	serverContext, stopServer := context.WithCancel(t.Context())
	defer stopServer()
	server := exec.CommandContext(serverContext, binary, "server")
	server.Stderr = tty
	server.Cancel = func() error { return server.Process.Signal(syscall.SIGTERM) }
	server.WaitDelay = 10 * time.Second
	ready, cancelReady := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancelReady()
	defer func() {
		if server.Process == nil {
			return
		}
		stopServer()
		if err := server.Wait(); err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("preview server shutdown: %v", err)
		}
	}()
	if err := sessionapi.EnsureControl(ready, socket, server.Start); err != nil {
		t.Fatal(err)
	}

	ui := exec.Command(binary, "codex", "-p", "preview")
	ui.Dir = workspace
	ui.Stdin, ui.Stdout, ui.Stderr = tty, tty, tty
	if err := ui.Run(); err != nil {
		t.Fatal(err)
	}
}

type walkthroughProvider struct {
	next atomic.Uint64
}

type walkthroughInput struct {
	Type    string         `json:"type"`
	Role    string         `json:"role"`
	CallID  string         `json:"call_id"`
	Output  jsontext.Value `json:"output"`
	Content []struct {
		Text string `json:"text"`
	} `json:"content"`
}

const walkthroughReview = "Review the demo pane change."

// Match the newest prompt or a named tool result in Codex's history. No
// request counter controls the scenario, so retries and agents can interleave.
func walkthroughStep(input []walkthroughInput) string {
	for i := len(input) - 1; i >= 0; i-- {
		item := input[i]
		switch item.Type {
		case "custom_tool_call_output", "function_call_output":
			if _, step, ok := strings.Cut(item.CallID, "-step-"); ok {
				if step == "sync-question" {
					var answer string
					if json.Unmarshal(item.Output, &answer) != nil || len(binding.QuestionReplies(binding.QuestionReplyStart+answer+binding.QuestionReplyEnd)) != 1 {
						return "invalid-sync-answer"
					}
				}
				return step
			}
		case "agent_message":
			for _, part := range item.Content {
				if strings.HasSuffix(strings.TrimSpace(part.Text), "\nPayload:\n"+walkthroughReview) {
					return "start-review"
				}
			}
		case "message":
			if item.Role != "user" {
				continue
			}
			for _, part := range item.Content {
				prompt := strings.TrimSpace(part.Text)
				if len(binding.QuestionReplies(prompt)) > 0 {
					return "async-answer"
				}
				if strings.HasPrefix(prompt, binding.QuestionReplyStart) {
					return "invalid-async-answer"
				}
				if prompt != "" && !strings.HasPrefix(prompt, "<") {
					switch prompt {
					case "demo":
						return "start-demo"
					case "questions":
						return "start-questions"
					default:
						return ""
					}
				}
			}
		}
	}
	return ""
}

func (p *walkthroughProvider) serve(w http.ResponseWriter, request *http.Request) {
	var input struct {
		Items []walkthroughInput `json:"input"`
	}
	if err := json.UnmarshalRead(request.Body, &input); err != nil {
		http.Error(w, "invalid preview request", http.StatusBadRequest)
		return
	}

	sequence := strconv.FormatUint(p.next.Add(1), 10)
	id := "preview-" + sequence
	message := func(text string) map[string]any {
		return map[string]any{
			"id": id + "-message", "type": "message", "role": "assistant",
			"status": "completed", "phase": "final_answer",
			"content": []any{map[string]any{"type": "output_text", "text": text}},
		}
	}
	execute := func(step, program string) map[string]any {
		return map[string]any{
			"id": id + "-exec", "type": "custom_tool_call", "status": "completed",
			"name": "exec", "namespace": "functions", "call_id": id + "-step-" + step, "input": program,
		}
	}
	question := func(step, name, arguments string) map[string]any {
		return map[string]any{
			"id": id + "-question", "type": "function_call", "status": "completed",
			"name": name, "call_id": id + "-step-" + step, "arguments": arguments,
		}
	}
	var item map[string]any
	summary := ""
	step := walkthroughStep(input.Items)
	switch step {
	case "start-demo":
		summary = "I’ll make the pane return immediately when its first frame isn’t ready."
		item = execute("edited", "text(await tools.apply_patch(`"+nativePreviewLaunchPatch+"`));")
	case "edited":
		item = execute("delegated", `text(await tools.collaboration__spawn_agent({task_name:"reviewer_`+sequence+`",message:`+
			strconv.Quote(walkthroughReview)+`,fork_turns:"none"})); text(await tools.collaboration__wait_agent({wait_mode:"all"}));`)
	case "start-review":
		item = execute("reviewed", `text(await tools.exec_command({cmd:"sed -n '1,40p' internal/pane/launch.go"}));`)
	case "reviewed":
		item = message("The pane now returns pending when the first frame isn’t ready.")
	case "delegated":
		item = message("The pane no longer blocks on its first frame. Inspect launch.go in Saved Diff.")
	case "start-questions":
		item = question("async-question", "request_user_input_async",
			`{"questions":[{"header":"Audience","question":"Who should receive the release update?","options":[
				{"label":"Customers","description":"Publish externally","preview":"Customer release update\n\nAnnounce the release to all customers."},
				{"label":"Internal team","description":"Review before publishing","preview":"Internal release update\n\nShare with the team first.\nCollect feedback before the public announcement.","recommended":true}]},
				{"header":"Timing","question":"When should the update go out?","options":[
				{"label":"Immediately","description":"Send when ready","preview":"Immediate delivery\n\nSend the update as soon as the release is ready."},
				{"label":"After validation","description":"Wait for release checks","preview":"Validated delivery\n\n1. Complete release checks.\n2. Confirm the canary is healthy.\n3. Send the update.","recommended":true}]}]}`)
	case "async-question":
		item = question("sync-question", "request_user_input",
			`{"questions":[{"header":"Scope","question":"Which release scope?","options":[
				{"label":"Narrow","description":"Only the affected path","preview":"Narrow release\n\nValidate and publish only the affected path."},
				{"label":"Broad","description":"Every path","preview":"Broad release\n\n1. Validate every release path.\n2. Publish the complete release.\n3. Monitor the rollout.","recommended":true}]}]}`)
	case "sync-question":
		item = message("Release scope received.")
	case "async-answer":
		item = message("Release update answers received.")
	case "invalid-sync-answer", "invalid-async-answer":
		item = message("The question answer was missing or invalid.")
	default:
		item = message("This is a scripted preview. Submit demo or questions.")
	}
	items := []map[string]any{item}
	if summary != "" {
		items = append([]map[string]any{{
			"id": id + "-reasoning", "type": "reasoning",
			"summary": []any{map[string]any{"type": "summary_text", "text": summary}},
		}}, items...)
	}
	w.Header().Set("Content-Type", "text/event-stream")
	stream := http.NewResponseController(w)
	emit := func(event map[string]any) bool {
		_, err := io.WriteString(w, routerFaultSSE(event))
		return err == nil && stream.Flush() == nil
	}
	if !emit(map[string]any{"type": "response.created", "response": map[string]any{
		"id": id, "status": "in_progress", "output": []any{},
	}}) {
		return
	}
	for index, output := range items {
		initial := maps.Clone(output)
		switch output["type"] {
		case "reasoning":
			initial["summary"] = []any{}
		case "custom_tool_call":
			initial["input"] = ""
			initial["status"] = "in_progress"
		case "function_call":
			initial["arguments"] = ""
			initial["status"] = "in_progress"
		}
		if !emit(map[string]any{"type": "response.output_item.added", "output_index": index, "item": initial}) {
			return
		}
		switch output["type"] {
		case "reasoning":
			if !emit(map[string]any{"type": "response.reasoning_summary_part.added", "output_index": index,
				"item_id": output["id"], "summary_index": 0, "part": map[string]any{"type": "summary_text", "text": ""}}) ||
				!emit(map[string]any{"type": "response.reasoning_summary_text.delta", "output_index": index,
					"item_id": output["id"], "summary_index": 0, "delta": summary}) {
				return
			}
		case "function_call":
			if !emit(map[string]any{"type": "response.function_call_arguments.done", "output_index": index,
				"item_id": output["id"], "arguments": output["arguments"]}) {
				return
			}
		case "custom_tool_call":
			program := output["input"].(string)
			for start := 0; start < len(program); start += 45 {
				if step == "start-demo" {
					select {
					case <-request.Context().Done():
						return
					case <-time.After(240 * time.Millisecond):
					}
				}
				if !emit(map[string]any{"type": "response.custom_tool_call_input.delta", "output_index": index,
					"item_id": output["id"], "delta": program[start:min(start+45, len(program))]}) {
					return
				}
			}
			if step == "start-demo" {
				// Give the terminal time to paint the last provisional frame
				// before Codex executes the patch and publishes the saved edit.
				select {
				case <-request.Context().Done():
					return
				case <-time.After(time.Second):
				}
			}
			if !emit(map[string]any{"type": "response.custom_tool_call_input.done", "output_index": index,
				"item_id": output["id"], "call_id": output["call_id"], "input": program}) {
				return
			}
		}
		if !emit(map[string]any{"type": "response.output_item.done", "output_index": index, "item": output}) {
			return
		}
	}
	emit(map[string]any{"type": "response.completed", "response": map[string]any{
		"id": id, "status": "completed", "output": items,
		"usage": map[string]any{"input_tokens": 100, "output_tokens": 50, "total_tokens": 150},
	}})
}
