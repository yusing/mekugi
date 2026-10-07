//go:build unix

package router

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/claude"
	"github.com/yusing/mekugi/internal/session"
)

// Installed Claude owns Bash and cancellation. Only the provider is scripted;
// file gates advance after the shared terminal renders the corresponding output.
func TestNativeRuntimeSegmentsClaudePTY(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_NATIVE_CLAUDE") != "1" {
		t.Skip("requires installed native Claude and built bridge")
	}
	for _, mode := range []string{"success", "failure", "interrupt", "stop"} {
		t.Run(mode, func(t *testing.T) { nativeRuntimeSegmentsClaudePTY(t, mode) })
	}
}

func nativeRuntimeSegmentsClaudePTY(t *testing.T, mode string) {
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	t.Setenv(routerTestWorkerEnvironment, "1")
	t.Setenv("MEKUGI_RUNTIME_DIR", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("ANTHROPIC_API_KEY", "native-segments-pty-fixture")
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
	const first = "PTY_SEGMENT_CEDAR"
	const second = "PTY_SEGMENT_MAPLE"
	const third = "PTY_SEGMENT_BIRCH"
	const fourth = "PTY_SEGMENT_ELM"
	const last = "PTY_SEGMENT_OAK"
	script := "printf '%s\\n' \"$$\" > segment-pid.txt\nprintf '%s\\n' " + second + "\nwhile [ ! -f segments-next.gate ]; do sleep 0.05; done\nprintf '%s\\n' " + third + "\nwhile [ ! -f segments-activity.gate ]; do sleep 0.05; done\nprintf '%s\\n' " + fourth + "\nwhile [ ! -f segments-finish.gate ]; do sleep 0.05; done\nprintf 'finished\\n' >> segment-finished.txt\n"
	if err := os.WriteFile(filepath.Join(binding.Workspace, "segment-gates.sh"), []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	penultimate := "true"
	if mode == "failure" {
		penultimate = "false"
	}
	command := "printf '" + first + "\\n'; printf 'once\\n' >> segment-effects.txt; bash ./segment-gates.sh && " + penultimate + " && printf '" + last + "\\n'"
	background := mode == "failure" || mode == "stop"
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
		mu.Lock()
		defer mu.Unlock()
		content := []any{map[string]any{"type": "text", "text": fmt.Sprintf("SEGMENT_PTY_ACCEPTED_%d", requests+1)}}
		stop := "end_turn"
		if tools, _ := packet["tools"].([]any); len(tools) > 0 {
			requests++
			if requests > 8 {
				t.Error("native segments exceeded scripted request budget")
				w.WriteHeader(400)
				return
			}
			if requests == 1 {
				content = []any{map[string]any{"type": "tool_use", "id": "segment-pty-once", "name": "Bash", "input": map[string]any{"command": command, "description": "Segment PTY gates", "run_in_background": background}}}
				stop = "tool_use"
			}
		}
		nativeGuidanceProviderReply(w, packet, content, stop)
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
	t.Cleanup(func() {
		// Release only this fixture's gates on failure, so cleanup owns no foreign process.
		_ = os.WriteFile(filepath.Join(binding.Workspace, "segments-next.gate"), nil, 0600)
		_ = os.WriteFile(filepath.Join(binding.Workspace, "segments-activity.gate"), nil, 0600)
		_ = os.WriteFile(filepath.Join(binding.Workspace, "segments-finish.gate"), nil, 0600)
		cancel()
		stop()
		<-joined
	})
	await := func(label string, match func(string) bool) {
		t.Helper()
		if !terminal.observe(25*time.Second, func(frame string) bool {
			if strings.Contains(frame, "Permission · Bash") && strings.Contains(frame, "Allow once") {
				terminal.keys("1\r")
			}
			return match(frame)
		}) {
			mu.Lock()
			for _, event := range events {
				t.Logf("native event: kind=%s id=%s failed=%v task=%+v", event.Kind, event.ID, event.Failed, event.Task)
			}
			mu.Unlock()
			t.Fatalf("missing %s:\n%s", label, terminal.screen.String())
		}
	}
	activity := false
	click := func(target string) {
		t.Helper()
		for y, row := range strings.Split(terminal.screen.String(), "\n") {
			runes := []rune(row)
			offset := 0
			if activity {
				offset = 60
			}
			if len(runes) <= offset {
				continue
			}
			end := min(len(runes), offset+60)
			visible := string(runes[offset:end])
			if at := strings.Index(visible, target); at >= 0 {
				x := offset + len([]rune(visible[:at])) + 1
				terminal.keys(fmt.Sprintf("\x1b[<0;%d;%dM\x1b[<0;%d;%dm", x, y+1, x, y+1))
				return
			}
		}
		t.Fatalf("no rendered click target %q:\n%s", target, terminal.screen.String())
	}
	gate := func(name string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(binding.Workspace, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	stateAwait := func(label string, match func(string) bool) {
		t.Helper()
		if !match(terminal.screen.String()) {
			await(label, match)
		}
	}
	dialog := func(s string) bool { return strings.Contains(s, "y copy · esc") }
	await("ready", func(s string) bool { return strings.Contains(s, "Ready") })
	terminal.paste("Execute the native segmented acceptance command once.")
	await("live segment disclosure before gate release", func(s string) bool {
		return strings.Contains(s, "Ran") && strings.Contains(s, first) && strings.Contains(s, "Running bash ./segment-gates.sh") && strings.Contains(s, second)
	})
	click(first)
	await("completed first-segment dialog", func(s string) bool {
		return dialog(s) && strings.Contains(s, "1 ┆ "+first) && !strings.Contains(s, "1 ┆ "+second) && !strings.Contains(s, "● live")
	})
	terminal.keys("\x1b[C\x1b[C")
	await("Main live segment dialog", func(s string) bool {
		return dialog(s) && strings.Contains(s, "1 ┆ "+second) && !strings.Contains(s, "1 ┆ "+first) && strings.Contains(s, "● live")
	})
	gate("segments-next.gate")
	await("Main open segment streams without replacement", func(s string) bool {
		return dialog(s) && strings.Contains(s, "1 ┆ "+second) && strings.Contains(s, "2 ┆ "+third) && strings.Contains(s, "● live")
	})
	terminal.keys("\x1b")
	await("Main updated segment", func(s string) bool { return !dialog(s) && strings.Contains(s, third) })
	terminal.keys("\x02" + "3")
	activity = true
	await("Activity live segment", func(s string) bool { return strings.Contains(s, "┌ 3 Activity") && strings.Contains(s, third) })
	click(third)
	await("Activity original live segment dialog", func(s string) bool {
		return dialog(s) && strings.Contains(s, "1 ┆ "+second) && strings.Contains(s, "2 ┆ "+third) && strings.Contains(s, "● live")
	})
	gate("segments-activity.gate")
	await("Activity open segment streams without replacement", func(s string) bool {
		return dialog(s) && strings.Contains(s, "1 ┆ "+second) && strings.Contains(s, "3 ┆ "+fourth) && strings.Contains(s, "● live")
	})
	switch mode {
	case "interrupt":
		terminal.keys("\x1b")
		await("closed before interrupt", func(s string) bool { return !dialog(s) })
		terminal.keys("\x02" + "1")
		terminal.keys("\x03")
		await("native foreground interruption", func(s string) bool {
			return strings.Contains(s, "Turn ended") && !strings.Contains(s, "Running bash ./segment-gates.sh") && !strings.Contains(s, "skipped") && !strings.Contains(s, "exit 0")
		})
	case "stop":
		if !strings.Contains(terminal.screen.String(), "x stop") {
			t.Fatal("segment dialog lacks native background stop affordance")
		}
		terminal.keys("x")
		await("native background stop settlement", func(s string) bool { return !strings.Contains(s, "● live") && !strings.Contains(s, "x stop") })
		terminal.keys("\x1b")
		await("background stopped Events", func(s string) bool { return !dialog(s) && strings.Contains(s, "Segment PTY gates · stopped") })
		click("Segment PTY gates · stopped")
		await("stopped Events dialog", func(s string) bool { return dialog(s) && strings.Contains(s, "Segment PTY gates · stopped") })
	default:
		gate("segments-finish.gate")
		await("Activity segment dialog settles in place", func(s string) bool {
			return dialog(s) && strings.Contains(s, "1 ┆ "+second) && strings.Contains(s, "2 ┆ "+third) && strings.Contains(s, "3 ┆ "+fourth) && !strings.Contains(s, "● live")
		})
		terminal.keys("\x1b")
		finalReply := "SEGMENT_PTY_ACCEPTED_2"
		if background {
			finalReply = "SEGMENT_PTY_ACCEPTED_3"
		}
		await("native final turn rendered", func(s string) bool {
			return !dialog(s) && strings.Contains(s, finalReply) && strings.Contains(s, "Ready")
		})
		stateAwait("final segment rows", func(s string) bool {
			if dialog(s) {
				return false
			}
			if mode == "failure" {
				return strings.Contains(s, "false · exit 1") && strings.Contains(s, "skipped")
			}
			return strings.Contains(s, "true") && strings.Contains(s, last)
		})
		if background {
			stateAwait("failed task Events", func(s string) bool { return strings.Contains(s, "Segment PTY gates · failed") })
			click("Segment PTY gates · failed")
			await("Activity Events dialog", func(s string) bool { return dialog(s) && strings.Contains(s, "Segment PTY gates · failed") })
			terminal.keys("\x1b")
			await("Events closed", func(s string) bool { return !dialog(s) })
		}
		terminal.keys("\x02" + "1")
		terminal.keys("\x1b[5~\x1b[5~")
		activity = false
		await("Main final segments", func(s string) bool {
			var rows []string
			for _, row := range strings.Split(s, "\n") {
				r := []rune(row)
				rows = append(rows, string(r[:min(60, len(r))]))
			}
			main := strings.Join(rows, "\n")
			return strings.Contains(main, third) && (mode != "failure" || strings.Contains(main, "skipped"))
		})
		click(third)
		await("Main final original segment dialog", func(s string) bool {
			return dialog(s) && strings.Contains(s, "1 ┆ "+second) && strings.Contains(s, "2 ┆ "+third) && strings.Contains(s, "3 ┆ "+fourth) && !strings.Contains(s, "● live")
		})
		if background {
			terminal.keys("\x1b")
			await("Main Events target", func(s string) bool { return !dialog(s) && strings.Contains(s, "Segment PTY gates · failed") })
			click("Segment PTY gates · failed")
			await("Main Events dialog", func(s string) bool { return dialog(s) && strings.Contains(s, "Segment PTY gates · failed") })
		}
	}
	if mode == "interrupt" || mode == "stop" {
		pidData, err := os.ReadFile(filepath.Join(binding.Workspace, "segment-pid.txt"))
		if err != nil {
			t.Fatal(err)
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(pidData)))
		if err != nil || pid <= 1 {
			t.Fatalf("invalid native script PID: %q %v", pidData, err)
		}
		deadline := time.NewTimer(5 * time.Second)
		defer deadline.Stop()
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		for {
			err := syscall.Kill(pid, 0)
			if errors.Is(err, syscall.ESRCH) {
				break
			}
			if err != nil {
				t.Fatalf("checking native Bash process %d: %v", pid, err)
			}
			select {
			case <-tick.C:
			case <-deadline.C:
				t.Fatalf("native cancellation left gated Bash process %d alive", pid)
			}
		}
		if _, err := os.Stat(filepath.Join(binding.Workspace, "segment-finished.txt")); !os.IsNotExist(err) {
			t.Fatalf("cancelled native command reached gated final effect: %v", err)
		}
	}
	effects, err := os.ReadFile(filepath.Join(binding.Workspace, "segment-effects.txt"))
	if err != nil || string(effects) != "once\n" {
		t.Fatalf("native effect not exactly once: %q %v", effects, err)
	}
	mu.Lock()
	defer mu.Unlock()
	tools, results := map[string]bool{}, 0
	var stopped, interrupted bool
	for _, event := range events {
		if event.Kind == "tool" && event.Role == "Bash" {
			tools[event.ID] = true
		}
		if event.Kind == "tool_result" && event.ID == "segment-pty-once" {
			results++
			interrupted = interrupted || event.Failed
		}
		if event.Task != nil && event.Task.ToolID == "segment-pty-once" && event.Task.Status == "stopped" {
			stopped = true
		}
	}
	if len(tools) != 1 || !tools["segment-pty-once"] {
		t.Fatalf("native call identities: %v", tools)
	}
	if mode == "interrupt" && !interrupted {
		t.Fatal("UI interruption lacked native failed tool receipt")
	}
	if mode == "stop" && !stopped {
		t.Fatal("UI settlement lacked native stopped task receipt")
	}
	if mode == "success" && results != 1 {
		t.Fatalf("native foreground terminal results=%d", results)
	}
}
