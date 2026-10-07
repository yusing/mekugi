package claude

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/session"
)

// No inference: the installed engine owns child history, delivery, permissions,
// effects and resume. A receipt is tested separately from child settlement.
func TestClaudeNativeDirectAgentMessages(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_NATIVE_CLAUDE") != "1" {
		t.Skip("requires installed native Claude and built bridge; local scripted provider only")
	}
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("ANTHROPIC_API_KEY", "native-agent-message-fixture")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	t.Setenv("CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC", "1")
	t.Setenv("CLAUDE_CODE_SHELL", "/bin/bash")
	const root = "DIRECT_ROOT_CEDAR"
	const original = "DIRECT_CHILD_ORIGINAL_BIRCH"
	const busy = "DIRECT_BUSY_MAPLE"
	const completed = "DIRECT_COMPLETED_OAK"
	const resumed = "DIRECT_RESUMED_PINE"
	release := make(chan struct{})
	childStarted := make(chan struct{})
	var childStartedOnce sync.Once
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	var mu sync.Mutex
	requests := 0
	packets := make(map[string]string)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			w.WriteHeader(401)
			return
		}
		var packet map[string]any
		if err := json.UnmarshalRead(http.MaxBytesReader(w, r.Body, 8<<20), &packet); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		encoded, _ := json.Marshal(packet["messages"])
		text := string(encoded)
		mu.Lock()
		requests++
		n := requests
		mu.Unlock()
		if n > 30 {
			t.Error("native child exceeded request budget")
			w.WriteHeader(400)
			return
		}
		content := []any{map[string]any{"type": "text", "text": "DIRECT_ROOT_DONE"}}
		stop := "end_turn"
		tool := func(id, name string, input map[string]any) {
			content = []any{map[string]any{"type": "tool_use", "id": id, "name": name, "input": input}}
			stop = "tool_use"
		}
		if strings.Contains(text, root) {
			if !strings.Contains(text, "direct-spawn") {
				tool("direct-spawn", "Agent", map[string]any{"description": "Direct message fixture child", "subagent_type": "general-purpose", "prompt": original, "run_in_background": true})
			}
		} else if strings.Contains(text, original) {
			phase := "initial"
			switch {
			case strings.Contains(text, resumed):
				phase = "resume"
			case strings.Contains(text, completed):
				phase = "completed"
			case strings.Contains(text, busy):
				phase = "busy"
			}
			mu.Lock()
			packets[phase] = text
			mu.Unlock()
			if phase == "initial" {
				childStartedOnce.Do(func() { close(childStarted) })
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
			}
			content = []any{map[string]any{"type": "text", "text": "DIRECT_CHILD_DONE_" + phase}}
			if phase != "initial" && !strings.Contains(text, "direct-effect-"+phase) {
				command := "printf '" + phase + "\\n' >> direct-effects.txt"
				if phase == "resume" {
					command = "printf forbidden > direct-denied.txt"
				}
				tool("direct-effect-"+phase, "Bash", map[string]any{"command": command, "description": "Direct message native effect " + phase})
			}
		}
		nativeAgentProviderReply(w, packet, content, stop)
	}))
	defer func() { unblock(); provider.Close() }()
	t.Setenv("ANTHROPIC_BASE_URL", provider.URL)
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Second)
	defer cancel()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	executable, err := exec.LookPath("claude")
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := filepath.Abs("bridge/dist/bridge.js")
	if err != nil {
		t.Fatal(err)
	}
	version, err := exec.CommandContext(ctx, executable, "--version").Output()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("installed native %s", strings.TrimSpace(string(version)))
	h := &nativeDecisionsHarness{ctx: ctx, workspace: t.TempDir()}
	if err := os.Mkdir(filepath.Join(h.workspace, ".claude"), 0700); err != nil {
		t.Fatal(err)
	}
	h.write(t, ".claude/settings.local.json", `{"permissions":{"defaultMode":"default","ask":["Bash"]}}`)
	h.client, err = Start(ctx, node, bridge, Config{Cwd: h.workspace, Executable: executable, Model: "haiku"})
	if err != nil {
		t.Fatal(err)
	}
	for e := h.next(t); e.Kind != "ready"; e = h.next(t) {
	}
	defer h.close(t)
	id, child := "", ""
	terminal := false
	seen := make(map[string]int)
	permissions := make(map[string]int)
	consume := func(e session.Event) {
		if e.Kind == "session" {
			id = e.SessionID
		}
		if e.Kind == "task" && e.Task != nil && (e.Role == "task_started" && e.Task.Kind == "local_agent" || e.Task.ID == child) {
			if child != "" && child != e.Task.ID {
				t.Fatalf("native continuation substituted child %s for %s", e.Task.ID, child)
			}
			child = e.Task.ID
			if e.Task.Status == "completed" {
				terminal = true
			}
		}
		if e.Kind == "prompt" {
			if e.Prompt == nil || e.Prompt.Tool != "Bash" {
				t.Fatalf("unexpected native permission: %+v", e.Prompt)
			}
			phase := "busy"
			if strings.Contains(e.Prompt.Description, "direct-denied.txt") {
				phase = "resume"
			} else if strings.Contains(e.Prompt.Description, "completed") {
				phase = "completed"
			}
			permissions[phase]++
			if err := h.client.Respond(h.ctx, session.Decision{ID: e.Prompt.ID, Allow: phase != "resume"}); err != nil {
				t.Fatal(err)
			}
		}
		if e.Kind == "message" && !e.Historical && strings.HasPrefix(e.Text, "DIRECT_CHILD_DONE_") {
			seen[strings.TrimPrefix(e.Text, "DIRECT_CHILD_DONE_")]++
		}
	}
	await := func(label string, match func(session.Event) bool) {
		t.Helper()
		for {
			e := h.next(t)
			consume(e)
			if match(e) {
				return
			}
		}
	}
	if err := h.client.Send(h.ctx, root); err != nil {
		t.Fatal(err)
	}
	await("native child", func(e session.Event) bool { return id != "" && child != "" })
	send := func(receiptID, sessionID, agentID, text string, failed bool) {
		t.Helper()
		command := session.AgentMessage{ID: receiptID, SessionID: sessionID, AgentID: agentID, Text: text}
		if err := h.client.SendAgentMessage(h.ctx, command); err != nil {
			t.Fatal(err)
		}
		await("delivery receipt", func(e session.Event) bool {
			if e.Kind != "agent_message" || e.AgentMessage == nil || e.AgentMessage.ID != receiptID {
				return false
			}
			if e.Failed != failed || e.AgentMessage.SessionID != sessionID || e.AgentMessage.AgentID != agentID || !failed && e.AgentMessage.Text != text {
				t.Fatalf("incorrect native delivery receipt: failed=%t message=%+v", e.Failed, e.AgentMessage)
			}
			return true
		})
	}
	select {
	case <-childStarted:
	case <-h.ctx.Done():
		t.Fatal(h.ctx.Err())
	}

	// Preserve the ordinary bridge frame contract above the removed 256 KiB
	// adapter-only restriction. Native delivery still owns its own limits.
	busyText := busy + "\n" + strings.Repeat("plain-child-text ", 17*1024)
	send("busy", id, child, busyText, false)
	if seen["busy"] != 0 || terminal {
		t.Fatal("queued receipt incorrectly implies child completion")
	}
	unblock()
	await("busy settlement", func(e session.Event) bool { return seen["busy"] > 0 && terminal })
	terminal = false
	send("completed", id, child, completed, false)
	await("same-child continuation", func(e session.Event) bool { return seen["completed"] > 0 && terminal })
	send("unknown", id, "unknown-native-child", "DO_NOT_DELIVER_UNKNOWN", true)
	savedID, savedChild := id, child
	if err := h.client.Close(); err != nil {
		t.Fatal(err)
	}
	h.client, err = Start(h.ctx, node, bridge, Config{Cwd: h.workspace, Executable: executable, Resume: savedID, Model: "haiku"})
	if err != nil {
		t.Fatal(err)
	}
	await("fresh bridge ready", func(e session.Event) bool { return e.Kind == "ready" })
	terminal = false
	send("resume", savedID, savedChild, resumed, false)
	await("resumed child denial", func(e session.Event) bool { return seen["resume"] > 0 && terminal })
	// Start a different native parent, then address the real saved child using
	// that parent's identity. Metadata, not a guessed session-ID mismatch, rejects it.
	if err := h.client.Close(); err != nil {
		t.Fatal(err)
	}
	h.client, err = Start(h.ctx, node, bridge, Config{Cwd: h.workspace, Executable: executable, Model: "haiku"})
	if err != nil {
		t.Fatal(err)
	}
	await("foreign bridge ready", func(e session.Event) bool { return e.Kind == "ready" })
	id = ""
	if err := h.client.Send(h.ctx, "DIRECT_FOREIGN_PARENT"); err != nil {
		t.Fatal(err)
	}
	await("foreign parent settled", func(e session.Event) bool { return id != "" && e.Kind == "done" })
	if id == savedID {
		t.Fatal("foreign parent reused original session")
	}
	send("foreign", id, savedChild, "DO_NOT_DELIVER_FOREIGN", true)
	// A native fork inherits displayed child history, not authority to continue
	// the source child's conversation through the new parent's message carrier.
	if err := h.client.Close(); err != nil {
		t.Fatal(err)
	}
	h.client, err = Start(h.ctx, node, bridge, Config{Cwd: h.workspace, Executable: executable, Resume: savedID, ForkSession: true, Model: "haiku"})
	if err != nil {
		t.Fatal(err)
	}
	await("fork bridge ready", func(e session.Event) bool { return e.Kind == "ready" })
	id = ""
	if err := h.client.Send(h.ctx, "DIRECT_FORK_PARENT"); err != nil {
		t.Fatal(err)
	}
	await("fork parent settled", func(e session.Event) bool { return id != "" && e.Kind == "done" })
	if id == savedID {
		t.Fatal("fork parent reused source identity")
	}
	send("fork-source", id, savedChild, "DO_NOT_DELIVER_FORK_SOURCE", true)
	h.wantFile(t, "direct-effects.txt", "busy\ncompleted\n")
	if _, err := os.Stat(filepath.Join(h.workspace, "direct-denied.txt")); !os.IsNotExist(err) {
		t.Fatalf("denied effect exists or stat failed: %v", err)
	}
	for _, phase := range []string{"busy", "completed", "resume"} {
		if seen[phase] != 1 || permissions[phase] != 1 {
			t.Fatalf("%s: completions=%d permissions=%d", phase, seen[phase], permissions[phase])
		}
	}
	mu.Lock()
	defer mu.Unlock()
	for _, phase := range []string{"busy", "completed", "resume"} {
		text := packets[phase]
		if !strings.Contains(text, original) || !strings.Contains(text, busy) {
			t.Fatalf("%s lost original child history", phase)
		}
		if phase == "resume" && (!strings.Contains(text, completed) || !strings.Contains(text, "Denied by the user")) {
			t.Fatal("fresh bridge lost completed child continuation or native denial result")
		}
		if strings.Contains(text, root) || strings.Contains(text, "DO_NOT_DELIVER_") {
			t.Fatalf("%s borrowed root or rejected-message context", phase)
		}
	}
	t.Log("installed-native busy queue, same-child continuation, fresh resume, unknown/foreign/fork-source rejection, exactly-once effects and explicit deny passed without inference")
}

func nativeAgentProviderReply(w http.ResponseWriter, packet map[string]any, content []any, stop string) {
	message := map[string]any{"id": "fixture-native-recovery", "type": "message", "role": "assistant", "model": "claude-haiku-4-5-20251001", "content": content, "stop_reason": stop, "usage": map[string]any{"input_tokens": 1, "output_tokens": 1}}
	if stream, _ := packet["stream"].(bool); !stream {
		w.Header().Set("Content-Type", "application/json")
		_ = json.MarshalWrite(w, message)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	emit := func(event string, value any) {
		encoded, _ := json.Marshal(value)
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, encoded)
	}
	message["content"] = []any{}
	message["stop_reason"] = nil
	emit("message_start", map[string]any{"type": "message_start", "message": message})
	for index, block := range content {
		value := block.(map[string]any)
		start := map[string]any{"type": value["type"]}
		var delta map[string]any
		if value["type"] == "tool_use" {
			start["id"], start["name"], start["input"] = value["id"], value["name"], map[string]any{}
			input, _ := json.Marshal(value["input"])
			delta = map[string]any{"type": "input_json_delta", "partial_json": string(input)}
		} else {
			start["text"] = ""
			delta = map[string]any{"type": "text_delta", "text": value["text"]}
		}
		emit("content_block_start", map[string]any{"type": "content_block_start", "index": index, "content_block": start})
		emit("content_block_delta", map[string]any{"type": "content_block_delta", "index": index, "delta": delta})
		emit("content_block_stop", map[string]any{"type": "content_block_stop", "index": index})
	}
	emit("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": stop, "stop_sequence": nil}, "usage": map[string]any{"output_tokens": 1}})
	_, _ = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
}
