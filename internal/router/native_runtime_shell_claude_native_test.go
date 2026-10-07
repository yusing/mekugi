package router

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/claude"
	"github.com/yusing/mekugi/internal/session"
)

func TestNativeRuntimeShellClaudePTY(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_NATIVE_CLAUDE") != "1" {
		t.Skip("requires installed native Claude and built bridge; local scripted provider only")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	t.Setenv(routerTestWorkerEnvironment, "1")
	t.Setenv("MEKUGI_RUNTIME_DIR", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("ANTHROPIC_API_KEY", "native-shell-pty-fixture")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	t.Setenv("CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC", "1")
	t.Setenv("CLAUDE_CODE_SHELL", "/bin/bash")
	var calls atomic.Int32
	packets := make(chan map[string]any, 8)
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
		calls.Add(1)
		packets <- packet
		nativeGuidanceProviderReply(w, packet, []any{map[string]any{"type": "text", "text": "SHELL_PTY_MAIN_ACCEPTED"}}, "end_turn")
	}))
	defer provider.Close()
	t.Setenv("ANTHROPIC_BASE_URL", provider.URL)
	binding := ObservationBinding{Runtime: "claude", Workspace: t.TempDir()}
	service, _, closeObservation := observationIsolationService(t, t.TempDir(), binding)
	defer closeObservation()
	presentation, err := service.PrepareCompanion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := service.Endpoint()
	companion := &claude.ObservationEndpoint{Socket: endpoint.Socket, Token: endpoint.Token, Plugin: presentation.Plugin, FrontendDirectory: presentation.FrontendDirectory, JournalSchema: presentation.JournalSchema}
	skillPath := filepath.Join(presentation.Plugin, "skills", "mekugi", "SKILL.md")
	skill, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatal(err)
	}
	_, workflow, ok := strings.Cut(string(skill), "\n---\n")
	if !ok {
		t.Fatal("active companion workflow has no body")
	}
	workflow = strings.TrimSpace(workflow)
	executable, err := exec.LookPath("claude")
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := filepath.Abs("../claude/bridge/dist/bridge.js")
	if err != nil {
		t.Fatal(err)
	}
	client, err := claude.Start(ctx, "node", bridge, claude.Config{Cwd: binding.Workspace, Executable: executable, Model: "haiku", Companion: companion})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	var saved atomic.Value
	observeClient := func(c *claude.Client) *nativeClaudePTYClient {
		o := &nativeClaudePTYClient{Client: c, events: make(chan session.Event)}
		go func() {
			defer close(o.events)
			for e := range c.Events() {
				if e.Kind == "session" && e.SessionID != "" {
					saved.Store(e.SessionID)
				}
				select {
				case o.events <- e:
				case <-ctx.Done():
					return
				}
			}
		}()
		return o
	}
	terminal, stop := startNativeRuntimePTY(t, ctx, observeClient(client), binding.Workspace, service)
	stopOnce := sync.OnceFunc(stop)
	defer stopOnce()
	await := func(label string, match func(string) bool) {
		t.Helper()
		if !terminal.observe(20*time.Second, match) {
			t.Fatalf("missing %s:\n%s", label, terminal.screen.String())
		}
	}
	write := func(name string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(binding.Workspace, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	click := func(text string) {
		t.Helper()
		for y, row := range strings.Split(terminal.screen.String(), "\n") {
			if at := strings.Index(row, text); at >= 0 {
				x := len([]rune(row[:at])) + 1
				terminal.keys(fmt.Sprintf("\x1b[<0;%d;%dM\x1b[<0;%d;%dm", x, y+1, x, y+1))
				return
			}
		}
		t.Fatalf("missing output click target %q", text)
	}
	await("ready", func(s string) bool { return strings.Contains(s, "Ready") })
	// Enter Shell Mode through the real composer, before the first model turn.
	terminal.keys("!printf SHELL_PTY_COMPLETE; printf 'once\\n' >> shell-pty-effects; while [ ! -f shell-pty-release ]; do sleep 0.05; done")
	await("Shell Mode", func(s string) bool { return strings.Contains(s, "Shell Mode") })
	terminal.keys("\r")
	await("native shell running", func(s string) bool {
		return strings.Contains(s, "printf SHELL_PTY_COMPLETE") && !strings.Contains(s, "Shell Mode")
	})
	terminal.keys("draft kept during shell")
	await("concurrent composer draft", func(s string) bool { return strings.Contains(s, "draft kept during shell") })
	terminal.keys("\r")
	await("submission lock", func(s string) bool {
		return strings.Contains(s, "draft kept") && strings.Contains(s, "draft kept during shell")
	})
	write("shell-pty-release")
	await("native shell completion", func(s string) bool {
		return strings.Contains(s, "context retained") && strings.Contains(s, "SHELL_PTY_COMPLETE")
	})
	if calls.Load() != 0 {
		t.Fatal("user shell queried model")
	}
	click("SHELL_PTY_COMPLETE")
	await("shell output dialog", func(s string) bool {
		return strings.Contains(s, "1 ┆ SHELL_PTY_COMPLETE") && strings.Contains(s, "y copy · esc")
	})
	terminal.keys("\x1b")
	await("draft after dialog", func(s string) bool {
		return strings.Contains(s, "draft kept during shell") && !strings.Contains(s, "y copy · esc")
	})
	// A fresh native bridge and UI must display saved user-shell rows before input.
	stopOnce()
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	resume, ok := saved.Load().(string)
	if !ok || resume == "" {
		t.Fatal("shell did not establish saved native identity")
	}
	client, err = claude.Start(ctx, "node", bridge, claude.Config{Cwd: binding.Workspace, Executable: executable, Resume: resume, Model: "haiku", Companion: companion})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	terminal, stop = startNativeRuntimePTY(t, ctx, observeClient(client), binding.Workspace, service)
	defer stop()
	await("resumed shell before input", func(s string) bool { return strings.Contains(s, "Ready") && strings.Contains(s, "SHELL_PTY_COMPLETE") })
	if calls.Load() != 0 {
		t.Fatal("shell replay queried model")
	}
	t.Log("shared PTY Shell Mode submit, draft retention, submission lock, output click/dialog and fresh replay before input passed without inference")
	terminal.paste("/model sonnet")
	await("native model control", func(s string) bool {
		return strings.Contains(s, "Native model selection accepted") && strings.Contains(s, "sonnet")
	})
	terminal.paste("/effort low")
	await("native effort control", func(s string) bool {
		return strings.Contains(s, "Effort override accepted") && strings.Contains(s, "effort request low")
	})
	const current = "SHELL_PTY_CURRENT_COMPANION_AFTER_HANDOFF"
	if err := os.WriteFile(skillPath, append(skill, []byte("\n"+current+"\n")...), 0600); err != nil {
		t.Fatal(err)
	}

	terminal.paste("!printf CANCEL_PTY; echo $$ > shell-pty-cancel.pid; while [ ! -f shell-pty-cancel-release ]; do sleep 0.05; done; printf forbidden > shell-pty-late")
	await("second shell running", func(s string) bool {
		return strings.Contains(s, "shell-pty-cancel.pid") && !strings.Contains(s, "Shell Mode")
	})
	var pid int
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		if data, err := os.ReadFile(filepath.Join(binding.Workspace, "shell-pty-cancel.pid")); err == nil {
			pid, err = strconv.Atoi(strings.TrimSpace(string(data)))
			if err != nil {
				t.Fatal(err)
			}
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("native cancellation process did not start")
	}
	defer func() {
		if pid > 0 {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}()
	terminal.keys("draft kept after cancellation")
	await("unsent cancellation draft", func(s string) bool { return strings.Contains(s, "draft kept after cancellation") })
	// Shared Codex precedence clears a nonempty draft first. Its undo record
	// must survive the native shutdown/resume triggered by the second Ctrl-C.
	terminal.keys("\x03")
	await("first Ctrl-C clears draft", func(s string) bool {
		return strings.Contains(s, "Draft cleared") && !strings.Contains(s, "draft kept after cancellation")
	})
	terminal.keys("\x03")
	await("Ctrl-C settles shell", func(s string) bool {
		return strings.Contains(s, "context retained") && strings.Contains(s, "sonnet") && strings.Contains(s, "effort request low") && syscall.Kill(pid, 0) == syscall.ESRCH
	})
	if err := syscall.Kill(pid, 0); err != syscall.ESRCH {
		t.Fatalf("Ctrl-C left native shell process alive: %v", err)
	}
	pid = 0 // Native cancellation already proved process death.
	terminal.keys("\x1a")
	await("unsent draft restored after handoff", func(s string) bool {
		return strings.Contains(s, "draft kept after cancellation") && strings.Contains(s, "sonnet") && strings.Contains(s, "effort request low")
	})
	write("shell-pty-cancel-release")
	if _, err := os.Stat(filepath.Join(binding.Workspace, "shell-pty-late")); !os.IsNotExist(err) {
		t.Fatalf("cancelled shell effect: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(binding.Workspace, "shell-pty-effects"))
	if err != nil || string(data) != "once\n" {
		t.Fatalf("user shell effects: %q %v", data, err)
	}
	if calls.Load() != 0 {
		t.Fatal("shell cancellation queried model")
	}
	terminal.keys("\r")
	await("input after native shutdown handoff", func(s string) bool {
		return strings.Contains(s, "SHELL_PTY_MAIN_ACCEPTED") && strings.Contains(s, "sonnet") && strings.Contains(s, "effort request low")
	})
	var packet map[string]any
	select {
	case packet = <-packets:
	case <-ctx.Done():
		t.Fatal("post-cancellation Main request missing")
	}
	carrier := nativeGuidanceRequestText(packet["system"]) + nativeGuidanceRequestText(packet["messages"])
	if !strings.Contains(carrier, workflow+"\n\n"+current) {
		t.Fatal("shutdown handoff lost current companion workflow")
	}
	assertNativeFrontendContracts(t, service.registry, carrier)
	if !strings.Contains(carrier, "draft kept after cancellation") || !strings.Contains(carrier, "SHELL_PTY_COMPLETE") || !strings.Contains(carrier, "CANCEL_PTY") || !strings.Contains(carrier, "The operation was aborted") {
		t.Fatal("shutdown handoff lost prior shell, cancellation or draft context")
	}
	model, _ := packet["model"].(string)
	if !strings.Contains(model, "sonnet") {
		t.Fatalf("handoff lost native selected model: %q", model)
	}
	if config, ok := packet["output_config"].(map[string]any); !ok || config["effort"] != "low" {
		t.Fatalf("handoff lost native effort request: %v", packet["output_config"])
	}
	service.owner.mu.Lock()
	ownerSession, ownerWorkspace := service.owner.session, service.owner.workspace
	_, owned := service.owner.bindings[ObservationBinding{Runtime: "claude", Session: resume, Workspace: binding.Workspace}]
	service.owner.mu.Unlock()
	if ownerSession != resume || ownerWorkspace != binding.Workspace || !owned {
		t.Fatalf("handoff changed companion ownership: session=%q workspace=%q bound=%t", ownerSession, ownerWorkspace, owned)
	}
	if calls.Load() != 1 {
		t.Fatalf("handoff replay or input repeated provider request: %d", calls.Load())
	}
	t.Log("shared PTY native shutdown retained unsent draft, selected model/effort, current workflow/frontends and same-session companion ownership")
}
