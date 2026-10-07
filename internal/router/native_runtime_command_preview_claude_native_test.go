//go:build unix

package router

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	mekugi "github.com/yusing/mekugi"
	"github.com/yusing/mekugi/internal/claude"
	"github.com/yusing/mekugi/internal/session"
)

// The local provider holds native Bash arguments open until the actual shared
// terminal has rendered growing proposals. Claude alone executes the command.
func TestNativeRuntimeCommandPreviewClaudePTY(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_NATIVE_CLAUDE") != "1" {
		t.Skip("requires installed native Claude and built bridge")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	t.Setenv(routerTestWorkerEnvironment, "1")
	t.Setenv("MEKUGI_RUNTIME_DIR", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("ANTHROPIC_API_KEY", "native-command-preview-fixture")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	t.Setenv("CLAUDE_CODE_SHELL", "/bin/bash")
	service, binding, _ := observationHTTPFixture(t)
	presentation, err := service.PrepareCompanion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	helper, err := execTrackHelper()
	if err != nil {
		t.Fatal(err)
	}
	bashEnv, err := service.PrepareCommandTracking(ctx, helper, "")
	if err != nil {
		t.Fatal(err)
	}
	var body strings.Builder
	for i := range 160 {
		fmt.Fprintf(&body, "NATIVE_PTY_ROW_%03d amber cedar violet\n", i)
	}
	path := filepath.Join(binding.Workspace, "bash-proposal.txt")
	prefix := "cat > bash-proposal.txt <<'EOF'\n"
	command := prefix + body.String() + "EOF\nprintf 'once\\n' >> preview-effects.txt"
	input, err := json.Marshal(map[string]any{"command": command, "description": "Bash preview acceptance"})
	if err != nil {
		t.Fatal(err)
	}
	// Locate exact escaped string offsets, without changing native arguments.
	firstText, _ := json.Marshal(prefix + strings.Join(strings.Split(body.String(), "\n")[:40], "\n") + "\n")
	grownText, _ := json.Marshal(prefix + body.String())
	firstEnd := strings.Index(string(input), string(firstText[1:len(firstText)-1])) + len(firstText) - 2
	grownEnd := strings.Index(string(input), string(grownText[1:len(grownText)-1])) + len(grownText) - 2
	if firstEnd <= 0 || grownEnd <= firstEnd {
		t.Fatal("invalid native stream boundaries")
	}
	grow, finish := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	requests := 0
	var events []session.Event
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var packet map[string]any
		if err := json.UnmarshalRead(http.MaxBytesReader(w, r.Body, 8<<20), &packet); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		tools, _ := packet["tools"].([]any)
		mu.Lock()
		if len(tools) > 0 {
			requests++
		}
		n := requests
		mu.Unlock()
		if n > 6 {
			t.Error("native preview exceeded scripted request budget")
			w.WriteHeader(400)
			return
		}
		if len(tools) == 0 || n != 1 {
			nativeGuidanceProviderReply(w, packet, []any{map[string]any{"type": "text", "text": "PREVIEW_NATIVE_COMPLETE"}}, "end_turn")
			return
		}
		if streaming, _ := packet["stream"].(bool); !streaming {
			t.Error("native request did not stream")
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		emit := func(kind string, value any) {
			data, _ := json.Marshal(value)
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, data)
			w.(http.Flusher).Flush()
		}
		emit("message_start", map[string]any{"type": "message_start", "message": map[string]any{"id": "fixture-preview", "type": "message", "role": "assistant", "model": "claude-haiku-4-5-20251001", "content": []any{}, "stop_reason": nil, "usage": map[string]any{"input_tokens": 1, "output_tokens": 1}}})
		emit("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "tool_use", "id": "preview-bash-once", "name": "Bash", "input": map[string]any{}}})
		delta := func(text string) {
			emit("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": text}})
		}
		delta(string(input[:firstEnd]))
		select {
		case <-grow:
		case <-r.Context().Done():
			return
		case <-ctx.Done():
			return
		}
		delta(string(input[firstEnd:grownEnd]))
		select {
		case <-finish:
		case <-r.Context().Done():
			return
		case <-ctx.Done():
			return
		}
		delta(string(input[grownEnd:]))
		emit("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
		emit("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "tool_use", "stop_sequence": nil}, "usage": map[string]any{"output_tokens": 1}})
		emit("message_stop", map[string]any{"type": "message_stop"})
	}))
	defer provider.Close()
	t.Setenv("ANTHROPIC_BASE_URL", provider.URL)
	executable, err := exec.LookPath("claude")
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := filepath.Abs("../claude/bridge/dist/bridge.js")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := service.Endpoint()
	client, err := claude.Start(ctx, "node", bridge, claude.Config{Cwd: binding.Workspace, Executable: executable, Model: "haiku", Companion: &claude.ObservationEndpoint{Socket: endpoint.Socket, Token: endpoint.Token, Plugin: presentation.Plugin, FrontendDirectory: presentation.FrontendDirectory, JournalSchema: presentation.JournalSchema, BashEnv: bashEnv}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	observed := &nativeClaudePTYClient{Client: client, events: make(chan session.Event)}
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		defer close(observed.events)
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-client.Events():
				if !ok {
					return
				}
				mu.Lock()
				events = append(events, event)
				mu.Unlock()
				select {
				case observed.events <- event:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	terminal, stop := startNativeRuntimePTY(t, ctx, observed, binding.Workspace, service)
	t.Cleanup(func() { cancel(); stop(); <-joined })
	await := func(label string, match func(string) bool) {
		t.Helper()
		if !terminal.observe(25*time.Second, func(frame string) bool {
			if strings.Contains(frame, "Permission · Bash") && strings.Contains(frame, "Allow once") {
				terminal.keys("\x02" + "1" + "1\r" + "\x02" + "2")
			}
			return match(frame)
		}) {
			t.Fatalf("missing %s:\n%s", label, terminal.screen.String())
		}
	}
	absent := func() {
		t.Helper()
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("native proposal preceded neither completion nor filesystem effects: %v", err)
		}
	}
	await("ready", func(s string) bool { return strings.Contains(s, "Ready") })
	terminal.paste("Execute the native Bash preview acceptance command once.")
	terminal.keys("\x02" + "2")
	await("open Bash input proposal", func(s string) bool {
		return strings.Contains(nativeClaudeDiffPane(s), "│+NATIVE_PTY_ROW_") && strings.Contains(s, "live")
	})
	absent()
	initial := nativeClaudeMaxRow(terminal.screen.String())
	close(grow)
	await("continued streamed proposal growth", func(s string) bool { return strings.Contains(s, "live") && nativeClaudeMaxRow(s) > initial+30 })
	absent()
	// Running duration advances independently of viewport navigation.
	stableMain := func(frame string) string {
		return regexp.MustCompile(`Running  · [^│]+`).ReplaceAllString(nativeClaudeMainPane(frame), "Running  · <elapsed>")
	}
	mainBefore := stableMain(terminal.screen.String())
	before := nativeClaudeMaxRow(terminal.screen.String())
	terminal.keys("\x1b[5~")
	await("independent proposal scroll", func(s string) bool {
		return strings.Contains(s, "live") && nativeClaudeMaxRow(s) >= 0 && nativeClaudeMaxRow(s) < before
	})
	if stableMain(terminal.screen.String()) != mainBefore {
		t.Fatalf("proposal scrolling changed Main viewport:\nbefore:\n%s\nafter:\n%s", mainBefore, nativeClaudeMainPane(terminal.screen.String()))
	}
	terminal.resize(72, 22)
	await("narrow streamed proposal", func(s string) bool {
		return nativeClaudeNarrowDiff(s) && strings.Contains(s, "live") && nativeClaudeMaxRow(s) >= 0
	})
	absent()
	terminal.resize(120, 36)
	close(finish)
	await("native saved reconciliation", func(s string) bool {
		return strings.Contains(s, "Ready") && strings.Contains(s, "saved") && strings.Contains(nativeClaudeDiffPane(s), "│+NATIVE_PTY_ROW_")
	})
	actual, err := os.ReadFile(path)
	if err != nil || string(actual) != body.String() {
		t.Fatalf("native heredoc effects: %q %v", actual, err)
	}
	effects, err := os.ReadFile(filepath.Join(binding.Workspace, "preview-effects.txt"))
	if err != nil || string(effects) != "once\n" {
		t.Fatalf("native execution not exactly once: %q %v", effects, err)
	}
	mu.Lock()
	defer mu.Unlock()
	sessionID := ""
	results, partials := 0, 0
	tools := map[string]bool{}
	exactInput := false
	for _, event := range events {
		if event.SessionID != "" {
			sessionID = event.SessionID
		}
		if event.ID == "preview-bash-once" {
			switch event.Kind {
			case "tool":
				tools[event.ID] = true
				var nativeInput struct {
					Command string `json:"command"`
				}
				if json.Unmarshal([]byte(event.Text), &nativeInput) == nil && nativeInput.Command == command {
					exactInput = true
				}
			case "tool_result":
				results++
				if event.Failed {
					t.Fatal("native Bash failed")
				}
			case "command_preview":
				if event.CommandInput != nil && !event.CommandInput.Complete {
					partials++
				}
			}
		}
	}
	if len(tools) != 1 || !exactInput || results != 1 || partials < 2 || sessionID == "" {
		t.Fatalf("native receipts: tools=%v results=%d partials=%d session=%q", tools, results, partials, sessionID)
	}
	files, err := service.owner.store.liveDiffSnapshotFiles(t.Context(), liveDiffScope{Workspaces: map[string]map[string]bool{binding.Workspace: {observationThread(ObservationBinding{Runtime: "claude", Workspace: binding.Workspace, Session: sessionID}): true}}})
	if err != nil {
		t.Fatal(err)
	}
	saved := false
	for _, file := range files {
		if file.Path == path && len(file.Chunks) == 1 {
			want := mekugi.RenderReviewFile("", path, "", body.String())
			saved = file.Chunks[0].Change != "" && file.Chunks[0].Review.Diff == want.Diff
		}
	}
	if !saved {
		t.Fatal("actual native heredoc lacked independently saved capture")
	}
}
