package claude

import (
	"context"
	json "encoding/json/v2"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/session"
)

// These opt-in tests use three real prompts in total, native authentication and
// normal billing. They exercise the existing Go client/SDK bridge, not a proxy,
// companion, substitute executor or permission bypass. Temporary project-local
// ask rules make the explicit decision observable even with inherited allow rules.
func TestClaudeNativePermissionDecisions(t *testing.T) {
	h := newNativeDecisionsHarness(t)
	h.write(t, "native-decisions-effect.sh", "#!/bin/sh\nset -eu\nprintf '%s\\n' \"$1\" >> effects\nprintf 'NATIVE_DECISION_EFFECT_%s\\n' \"$1\"\n")
	for _, allow := range []bool{true, false} {
		name, before, after := "deny", "allow\n", "allow\n"
		if allow {
			name, before, after = "allow", "", "allow\n"
		}
		t.Run(name, func(t *testing.T) {
			command := "sh ./native-decisions-effect.sh " + name
			if err := h.client.Send(h.ctx, "Use Bash exactly once with command `"+command+"`. Do not change the command, run other tools, retry a denied call, or use an alternative. After its result, reply only DONE."); err != nil {
				t.Fatal(err)
			}
			var call nativeDecisionsCall
			results := 0
		loop:
			for {
				e := h.next(t)
				switch e.Kind {
				case "tool":
					call.tool(t, e, command, false)
				case "prompt":
					h.wantFile(t, "effects", before)
					call.permission(t, h, e, command, false, allow)
				case "tool_result":
					results++
					if e.ID != call.id || results != 1 || e.Failed == allow {
						t.Fatal("missing, duplicate, uncorrelated or incorrect native tool outcome")
					}
					if allow && !strings.Contains(e.Text, "NATIVE_DECISION_EFFECT_allow") {
						t.Fatal("allowed native result lost fixture stdout")
					}
					if !allow && !strings.Contains(e.Text, "Denied by the user") {
						t.Fatal("denied native result lost the explicit decision reason")
					}
				case "done":
					break loop
				}
			}
			call.complete(t)
			if results != 1 {
				t.Fatal("native terminal tool result missing")
			}
			h.wantFile(t, "effects", after)
			t.Logf("%s: one explicit permission, unchanged complete Bash input, one native result; effect ledger=%q", name, after)
		})
		if t.Failed() {
			break
		}
	}
	h.close(t)
	h.wantFile(t, "effects", "allow\n")
}

func TestClaudeNativeBackgroundTaskStop(t *testing.T) {
	h := newNativeDecisionsHarness(t)
	// Bounded even if native cancellation fails. Heartbeats independently expose
	// continued execution; only the native terminal task event establishes status.
	h.write(t, "native-decisions-background.sh", "#!/bin/sh\nset -eu\nprintf 'started\\n' >> starts\ni=0\nwhile [ \"$i\" -lt 300 ]; do\n  printf '. ' >> heartbeat\n  sleep 0.2\n  i=$((i + 1))\ndone\nprintf 'completed\\n' >> completed\n")
	const command = "sh ./native-decisions-background.sh"
	if err := h.client.Send(h.ctx, "Use Bash exactly once with command `"+command+"` and run_in_background=true. Do not change the command or use any other tools, including TaskOutput or TaskStop. Once Bash returns its background result, reply only STARTED. The client will stop the task using the native control."); err != nil {
		t.Fatal(err)
	}
	var call nativeDecisionsCall
	taskID, terminalRole, terminalStatus := "", "", ""
	rootDone, stopSent := false, false
	results, acknowledgements := 0, 0
	for acknowledgements == 0 || terminalStatus == "" {
		e := h.next(t)
		switch e.Kind {
		case "tool":
			call.tool(t, e, command, true)
		case "prompt":
			h.wantFile(t, "starts", "")
			call.permission(t, h, e, command, true, true)
		case "tool_result":
			results++
			if e.ID != call.id || e.Failed || results != 1 {
				t.Fatal("background launch did not return one successful correlated native result")
			}
		case "task":
			if e.Task == nil || e.Task.ID == "" {
				t.Fatal("native task identity missing")
			}
			if e.Role == "task_started" {
				if taskID != "" || e.Task.ToolID != call.id || e.Task.Kind != "local_bash" || e.Task.Status != "running" {
					t.Fatal("missing, duplicate or uncorrelated native Bash task start")
				}
				taskID = e.Task.ID
			}
			if e.Task.ID != taskID {
				t.Fatal("unexpected native task identity")
			}
			switch e.Task.Status {
			case "stopped", "killed":
				if !stopSent || e.Role != "task_notification" && e.Role != "task_updated" {
					t.Fatal("task settled without a client stop or native terminal event")
				}
				terminalRole, terminalStatus = e.Role, e.Task.Status
			case "completed", "failed":
				t.Fatalf("task ended with %s instead of native stop", e.Task.Status)
			}
		case "done":
			rootDone = true // Never a substitute for task settlement.
		case "task_control":
			acknowledgements++
			if !stopSent || e.ID != taskID || e.Failed || acknowledgements != 1 {
				t.Fatal("native task stop acknowledgement failed or mismatched")
			}
		}
		if rootDone && taskID != "" && results == 1 && !stopSent {
			call.complete(t)
			h.wantFile(t, "starts", "started\n")
			h.wantFile(t, "completed", "")
			stopSent = true
			if err := h.client.StopTask(h.ctx, taskID); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Check while the bridge is still alive: closing it must not be what stops
	// the effect. One fixed observation window, not an unbounded polling loop.
	heartbeat := h.read(t, "heartbeat")
	if len(heartbeat) == 0 {
		t.Fatal("background fixture supplied no actual running-effect evidence")
	}
	settlement := time.NewTimer(time.Second)
	defer settlement.Stop()
observe:
	for {
		select {
		case e, ok := <-h.client.Events():
			if !ok || e.Kind == "error" || e.Kind == "done" && e.Failed {
				t.Fatal("native bridge failed during stop settlement")
			}
			if e.Kind == "tool" || e.Kind == "prompt" || e.Kind == "tool_result" || e.Kind == "task_control" {
				t.Fatalf("unexpected additional native %s after stop settlement", e.Kind)
			}
		case <-settlement.C:
			break observe
		case <-h.ctx.Done():
			t.Fatal(h.ctx.Err())
		}
	}
	h.wantFile(t, "heartbeat", heartbeat)
	h.wantFile(t, "starts", "started\n")
	h.wantFile(t, "completed", "")
	h.close(t)
	t.Logf("one native background effect; root completed before StopTask; one acknowledgement; terminal %s/%s; heartbeat stopped before client shutdown", terminalRole, terminalStatus)
}

type nativeDecisionsHarness struct {
	ctx       context.Context
	client    *Client
	workspace string
}

func newNativeDecisionsHarness(t *testing.T) *nativeDecisionsHarness {
	t.Helper()
	if os.Getenv("MEKUGI_TEST_NATIVE_CLAUDE") != "1" {
		t.Skip("set MEKUGI_TEST_NATIVE_CLAUDE=1 for real native permission/task-stop acceptance")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	t.Cleanup(cancel)
	executable, err := exec.LookPath("claude")
	if err != nil {
		t.Fatal(err)
	}
	version, err := exec.CommandContext(ctx, executable, "--version").Output()
	if err != nil || strings.TrimSpace(string(version)) != "2.1.287 (Claude Code)" {
		t.Fatal("live acceptance requires installed Claude CLI 2.1.287")
	}
	for _, path := range []string{"bridge/package.json", "bridge/node_modules/@anthropic-ai/claude-agent-sdk/package.json"} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal("run make test-claude before live acceptance: ", err)
		}
		var pkg struct {
			Version      string            `json:"version"`
			Dependencies map[string]string `json:"dependencies"`
		}
		if err := json.Unmarshal(data, &pkg); err != nil {
			t.Fatal(err)
		}
		if path == "bridge/package.json" {
			pkg.Version = pkg.Dependencies["@anthropic-ai/claude-agent-sdk"]
		}
		if pkg.Version != "0.3.287" {
			t.Fatal("live acceptance requires declared and installed SDK 0.3.287")
		}
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := filepath.Abs("bridge/dist/bridge.js")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(bridge); err != nil {
		t.Fatal("run make test-claude before live acceptance: ", err)
	}
	h := &nativeDecisionsHarness{ctx: ctx, workspace: t.TempDir()}
	if err := os.Mkdir(filepath.Join(h.workspace, ".claude"), 0700); err != nil {
		t.Fatal(err)
	}
	h.write(t, ".claude/settings.local.json", `{"permissions":{"defaultMode":"default","ask":["Bash"]}}`)
	h.client, err = Start(ctx, node, bridge, Config{Cwd: h.workspace, Executable: executable, Model: "haiku"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.client.Close() })
	for e := h.next(t); e.Kind != "ready"; e = h.next(t) {
	}
	t.Log("installed CLI 2.1.287; declared/installed SDK 0.3.287; isolated temporary project ask rules; native haiku model")
	return h
}

func (h *nativeDecisionsHarness) next(t *testing.T) session.Event {
	t.Helper()
	select {
	case e, ok := <-h.client.Events():
		if !ok || e.Kind == "error" || e.Kind == "done" && e.Failed {
			t.Fatalf("native bridge disconnected or failed (kind=%q, failed=%t)", e.Kind, e.Failed)
		}
		return e
	case <-h.ctx.Done():
		t.Fatal("native acceptance deadline: ", h.ctx.Err())
		return session.Event{}
	}
}

func (h *nativeDecisionsHarness) write(t *testing.T, path, text string) {
	t.Helper()
	// Paths and contents here are static fixture data, never native output.
	if err := os.WriteFile(filepath.Join(h.workspace, path), []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}

func (h *nativeDecisionsHarness) read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(h.workspace, path))
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func (h *nativeDecisionsHarness) wantFile(t *testing.T, path, want string) {
	t.Helper()
	if h.read(t, path) != want {
		t.Fatalf("unexpected actual fixture effect in %s", path)
	}
}

func (h *nativeDecisionsHarness) close(t *testing.T) {
	t.Helper()
	if err := h.client.Close(); err != nil {
		t.Fatal("native bridge shutdown: ", err)
	}
}

// The presentation Prompt currently carries input JSON in Description, not a
// separate input/tool-use-ID field. Compare the whole JSON value against the
// sole native call in this bounded fixture; do not invent absent correlation.
type nativeDecisionsCall struct {
	id          string
	input       map[string]any
	approved    map[string]any
	permissions int
}

func nativeDecisionsInput(t *testing.T, text, command string, background bool) map[string]any {
	t.Helper()
	var input map[string]any
	if err := json.Unmarshal([]byte(text), &input); err != nil {
		t.Fatal("native complete tool input is not JSON")
	}
	if input["command"] != command {
		t.Fatal("native input differs from exact fixture command")
	}
	if value, ok := input["dangerouslyDisableSandbox"]; ok && value != false {
		t.Fatal("native fixture must not bypass sandbox policy")
	}
	if value, ok := input["run_in_background"]; background && value != true || !background && ok && value != false {
		t.Fatal("native tool input has unexpected background mode")
	}
	return input
}

func (c *nativeDecisionsCall) tool(t *testing.T, e session.Event, command string, background bool) {
	t.Helper()
	if e.Role != "Bash" || e.ID == "" || c.id != "" && e.ID != c.id {
		t.Fatal("unexpected tool or additional native tool call")
	}
	c.id = e.ID
	if e.Text == "{}" || e.Text == "" {
		return // Streaming tool start does not yet contain complete input.
	}
	input := nativeDecisionsInput(t, e.Text, command, background)
	if c.input != nil && !reflect.DeepEqual(c.input, input) {
		t.Fatal("native complete tool input changed")
	}
	c.input = input
}

func (c *nativeDecisionsCall) permission(t *testing.T, h *nativeDecisionsHarness, e session.Event, command string, background, allow bool) {
	t.Helper()
	c.permissions++
	if e.Prompt == nil || e.Prompt.Tool != "Bash" || e.Prompt.ID == "" || c.permissions != 1 {
		t.Fatal("expected exactly one explicit native Bash permission")
	}
	_, input, ok := strings.Cut(e.Prompt.Description, "\n{")
	if !ok {
		t.Fatal("native permission input missing from presentation")
	}
	c.approved = nativeDecisionsInput(t, "{"+input, command, background)
	if err := h.client.Respond(h.ctx, session.Decision{ID: e.Prompt.ID, Allow: allow}); err != nil {
		t.Fatal(err)
	}
}

func (c *nativeDecisionsCall) complete(t *testing.T) {
	t.Helper()
	if c.id == "" || c.permissions != 1 || c.input == nil || !reflect.DeepEqual(c.input, c.approved) {
		t.Fatal("missing explicit permission or changed native tool input")
	}
}
