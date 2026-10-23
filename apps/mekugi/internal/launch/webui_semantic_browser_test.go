package launch

import (
	"context"
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/connector"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/webui"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/commandargs"
	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

// The visible browser consumes the real server and connector. Only Codex is a
// controlled subprocess; its ledger records the admitted semantic operations.
func TestWebUISemanticBrowser(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_WEBUI_SEMANTIC_HOST") == "1" {
		serveWebUISemanticHost(t)
		os.Exit(0)
	}
	if os.Getenv("MEKUGI_TEST_WEBUI_BROWSER") != "1" {
		t.Skip("set MEKUGI_TEST_WEBUI_BROWSER=1 after make webui, with agent-browser installed")
	}
	// Keep the browser separate from the execution host's per-test environment.
	browserEnvironment := append(os.Environ(), "AGENT_BROWSER_ENGINE=chrome")
	for _, key := range []string{"XDG_CONFIG_HOME", "XDG_STATE_HOME", "CODEX_HOME", "MEKUGI_RUNTIME_DIR"} {
		t.Setenv(key, t.TempDir())
	}
	for _, key := range []string{"XAI_API_KEY", "OPENCODE_API_KEY", "OPENCODE_GO_API_KEY", "OPENCODE_ZEN_API_KEY"} {
		t.Setenv(key, "")
	}
	writeFeatureConfig(t, "shell_guard = false")
	t.Setenv("AGENT_BROWSER_ENGINE", "chrome")
	directory, err := os.MkdirTemp("/tmp", "mekugi-webui-semantic-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(directory)
	workspace := t.TempDir()
	ledger := filepath.Join(directory, "host.jsonl")
	t.Setenv("MEKUGI_TEST_WEBUI_SEMANTIC_LOG", ledger)
	bin := t.TempDir()
	script := "#!/bin/sh\nif [ \"$1\" = debug ]; then exit 1; fi\nMEKUGI_TEST_WEBUI_SEMANTIC_HOST=1; export MEKUGI_TEST_WEBUI_SEMANTIC_HOST\nexec " + commandargs.Quote(os.Args[0]) + " -test.run=^TestWebUISemanticBrowser$ -- \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	socket := filepath.Join(directory, "control.sock")
	serverContext, stopServer := context.WithCancel(ctx)
	launcher := &webUISemanticLauncher{serverLauncher: new(serverLauncher), failures: make(chan error, 4)}
	hub := sessionapi.NewHub(serverContext, launcher)
	listener, err := sessionapi.ListenControl(socket)
	if err != nil {
		t.Fatal(err)
	}
	serverDone := make(chan error, 1)
	go func() { serverDone <- serveSessionHub(serverContext, listener, hub) }()
	defer func() {
		stopServer()
		select {
		case err := <-serverDone:
			if err != nil {
				t.Errorf("server exit: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("server did not join its owned runtime")
		}
	}()
	serverCLIAwait(t, func() bool { _, err := os.Stat(socket); return err == nil })
	page := httptest.NewServer(webui.Handler())
	defer page.Close()
	const token = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	tokenFile := filepath.Join(directory, "token")
	if err := os.WriteFile(tokenFile, []byte(token+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	bridge, err := connector.New(connector.Options{SocketPath: socket, TokenFile: tokenFile, Origins: []string{page.URL}})
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.Close()
	remote := httptest.NewServer(bridge)
	defer remote.Close()
	browser := os.Getenv("MEKUGI_TEST_AGENT_BROWSER")
	if browser == "" {
		browser = "agent-browser"
	}
	identityCommand := exec.CommandContext(ctx, browser, "--engine", "chrome", "session", "id", "--scope", "worktree", "--prefix", "webui-semantic")
	identityCommand.Env = browserEnvironment
	identity, err := identityCommand.CombinedOutput()
	if err != nil {
		t.Fatalf("browser identity: %v: %s", err, identity)
	}
	name := strings.TrimSpace(string(identity))
	command := func(ctx context.Context, args ...string) *exec.Cmd {
		command := exec.CommandContext(ctx, browser, append([]string{"--engine", "chrome", "--session", name}, args...)...)
		command.Env = browserEnvironment
		return command
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_ = command(cleanup, "close").Run()
	}()
	run := func(args ...string) string {
		t.Helper()
		output, err := command(ctx, args...).CombinedOutput()
		if err != nil {
			_, _ = command(ctx, "doctor", "--offline", "--quick").CombinedOutput()
			snapshot, _ := command(ctx, "snapshot").CombinedOutput()
			problems, _ := command(ctx, "errors").CombinedOutput()
			select {
			case failure := <-launcher.failures:
				t.Logf("execution owner: %v", failure)
			default:
			}
			body, _ := os.ReadFile(ledger)
			var methods []string
			for line := range strings.SplitSeq(string(body), "\n") {
				method, _, _ := strings.Cut(line, " ")
				if strings.HasPrefix(line, "fixture-error:") {
					method = line
				}
				methods = append(methods, method)
			}
			t.Fatalf("browser %s: %v: %s\n%s\n%s\nhost methods: %v", strings.Join(args[:min(2, len(args))], " "), err, output, snapshot, problems, methods)
		}
		return string(output)
	}
	button := func(label string) { t.Helper(); run("find", "role", "button", "click", "--name", label, "--exact") }
	message := func(text string) {
		t.Helper()
		run("wait", "--fn", "document.querySelector('#composer-text')?.disabled === false")
		run("find", "label", "Message", "fill", text)
		button("Send")
	}
	closeDialog := func() {
		t.Helper()
		button("Close dialog")
		run("wait", "--fn", "!document.querySelector('[role=dialog],dialog[open]')")
	}
	// Typing a trigger opens the server's completions above the composer.
	suggest := func(text string) {
		t.Helper()
		run("wait", "--fn", "document.querySelector('#composer-text')?.disabled === false")
		run("find", "label", "Message", "fill", text)
		run("wait", "--fn", "!!document.querySelector('#completions [role=option]')")
	}
	waitRecovery := func() {
		t.Helper()
		run("wait", "--text", "Original attachments are retained.")
		run("wait", "--fn", "document.querySelector('#composer-text')?.disabled === false && !!Array.from(document.querySelectorAll('button')).find(b => b.textContent.trim() === 'Retry intact' && !b.disabled)")
	}
	waitRecovered := func() {
		t.Helper()
		run("wait", "--fn", "!document.querySelector('[aria-label=\"Unsent submission\"]')")
	}
	// With MEKUGI_TEST_WEBUI_ARTIFACTS set, each captured state is saved at
	// desktop, tablet and phone sizes in both themes. The primary action must
	// stay on screen at its responsive control height, without sideways scroll.
	capture := func(state string) {
		t.Helper()
		artifacts := os.Getenv("MEKUGI_TEST_WEBUI_ARTIFACTS")
		if artifacts == "" {
			return
		}
		for _, size := range []struct{ name, width, height string }{{"desktop", "1280", "900"}, {"tablet", "768", "1024"}, {"phone", "390", "844"}} {
			run("set", "viewport", size.width, size.height)
			for _, theme := range []string{"dark", "light"} {
				run("set", "media", theme)
				if strings.TrimSpace(run("eval", "(() => { const b = document.querySelector('button[type=submit]'); if (!b) return true; const r = b.getBoundingClientRect(); return r.height >= (innerWidth <= 640 ? 44 : 32) && r.left >= 0 && r.right <= innerWidth && r.bottom <= innerHeight && document.documentElement.scrollWidth <= innerWidth; })()")) != "true" {
					t.Errorf("%s %s %s: the primary action must stay on screen at its responsive control height: %s", state, size.name, theme, run("eval", "document.querySelector('button[type=submit]')?.getBoundingClientRect().toJSON()"))
				}
				run("screenshot", filepath.Join(artifacts, state+"-"+size.name+"-"+theme+".png"))
			}
		}
		run("set", "viewport", "1280", "720")
	}
	run("open", page.URL)
	run("snapshot", "-i")
	capture("welcome")
	run("find", "label", "Machine name", "fill", "Execution")
	run("find", "label", "Connector URL", "fill", remote.URL)
	run("find", "label", "Access token", "fill", token)
	button("Connect")
	run("wait", "--text", "What should main work on?")
	run("wait", "--fn", "Array.from(document.querySelectorAll('[role=option]')).some(option => option.textContent.trim() === 'Default')")
	run("find", "role", "button", "click", "--name", "Workspace on Execution")
	run("find", "label", "Workspace path", "fill", workspace)
	run("press", "Enter")
	run("find", "role", "button", "click", "--name", "Model")
	run("find", "role", "option", "click", "--name", "Default", "--exact")
	run("find", "label", "First message", "fill", "browser first input")
	firstUpload := filepath.Join(directory, "first.txt")
	laterUpload := filepath.Join(directory, "later.txt")
	if err := os.WriteFile(firstUpload, []byte("first uploaded snapshot"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(laterUpload, []byte("later uploaded snapshot"), 0600); err != nil {
		t.Fatal(err)
	}
	run("upload", "input[type=file]", firstUpload, pasteTestImage(t))
	run("wait", "--text", "first.txt")
	capture("new-thread")
	button("Start thread")
	waitRecovery()
	// The first submission belongs to the newly attached browser view, so its
	// asynchronous rejection must return here with both uploaded files intact.
	capture("recovery")
	message("/status")
	run("wait", "--text", "Session status")
	closeDialog()
	waitRecovery()
	button("Retry intact")
	run("wait", "--text", "Fixture rejection 2")
	waitRecovery()
	button("Retry intact")
	waitRecovered()
	run("wait", "--text", "Browser host answer 1")
	run("find", "text", "Browser host answer 1", "text", "--exact")
	run("snapshot", "-i")
	t.Log("created through the empty-state composer with uploaded text and image")
	run("wait", "--fn", `document.querySelector('img[alt="Attached image 1"]')?.naturalWidth > 0`)
	run("wait", "--fn", `document.querySelector('.text-marker-image')?.textContent === '[Image 1]'`)
	run("wait", "--fn", `document.querySelector('h1')?.nextElementSibling?.getAttribute('aria-label') === 'Session details'`)
	run("find", "label", "Message", "fill", "draft survives fast mode")
	button("Fast mode")
	run("wait", "--fn", `document.querySelector('button[aria-label="Fast mode"]')?.getAttribute('aria-pressed') === 'true'`)
	button("Fast mode")
	run("wait", "--fn", `document.querySelector('button[aria-label="Fast mode"]')?.getAttribute('aria-pressed') === 'false' && document.querySelector('#composer-text')?.value === 'draft survives fast mode'`)
	run("find", "label", "Message", "fill", `!printf '%s\n' "$PATH"`)
	run("wait", "--fn", `document.querySelector('#composer-text')?.previousElementSibling?.querySelectorAll('span[style]').length > 1`)
	capture("shell-highlight")
	run("find", "label", "Shell command", "fill", "")
	t.Log("images, bound markers, fast toggle and shell highlighting passed")

	message("/status")
	run("wait", "--text", "Session status")
	run("wait", "--fn", "document.querySelector('#composer-text')?.value === '' && !document.querySelector('[role=alert]')")
	closeDialog()
	suggest("/")
	run("press", "Escape")
	run("wait", "--fn", "!document.querySelector('#completions')")
	suggest("/stat")
	capture("completions")
	run("find", "text", "/status", "click", "--exact")
	run("wait", "--fn", "document.querySelector('#composer-text')?.value.trim() === '/status'")
	button("Send")
	run("wait", "--text", "Session status")
	closeDialog()
	// A completed file keeps its mention and attaches its host path.
	suggest("see @note")
	run("press", "Enter")
	run("wait", "--fn", "document.querySelector('#composer-text')?.value === 'see @notes.md ' && !document.querySelector('[role=alert]')")
	button("Remove notes.md")
	run("find", "label", "Message", "fill", "")
	// Main's model and effort are composer menus of the thread's own choices.
	// Changing them leaves the draft in place.
	run("find", "label", "Message", "fill", "kept draft")
	run("click", `button[title="Model"]`)
	run("find", "role", "option", "click", "--name", "fixture-b", "--exact")
	run("wait", "--fn", "document.querySelector('button[title=Model]')?.textContent.includes('fixture-b') && !document.querySelector('[role=listbox][aria-label=Model]')")
	run("click", `button[title="Reasoning effort"]`)
	run("find", "role", "option", "click", "--name", "high", "--exact")
	run("wait", "--fn", "document.querySelector('button[title=\"Reasoning effort\"]')?.textContent.includes('high')")
	run("click", `button[title="Model"]`)
	run("wait", "--fn", "!!document.querySelector('[role=listbox][aria-label=Model] [aria-selected=true]')")
	capture("settings")
	run("press", "Escape")
	run("wait", "--fn", "!document.querySelector('[role=listbox][aria-label=Model]') && document.querySelector('#composer-text')?.value === 'kept draft' && !document.querySelector('[role=alert], #composer-hint.text-negative')")
	run("find", "label", "Message", "fill", "")
	message("/btw quick question")
	run("wait", "--text", "By the way")
	run("wait", "--text", "Browser host answer 2")
	button("Close answer")
	run("wait", "--fn", "!document.querySelector('[aria-label=\"Side answer\"]')")
	message("/side")
	run("wait", "--fn", "document.querySelectorAll('[role=tab]').length === 2")
	run("find", "role", "tab", "click", "--name", "Main", "--exact")
	run("wait", "--fn", "Array.from(document.querySelectorAll('[role=tab]')).find(tab => tab.textContent.trim() === 'Main')?.getAttribute('aria-selected') === 'true'")
	button("Close side")
	run("wait", "--fn", "!document.querySelector('[role=tab]')")
	run("upload", "input[type=file]", laterUpload)
	run("wait", "--text", "later.txt")
	message("browser later input")
	waitRecovery()
	suggest("/")
	run("press", "Escape")
	run("wait", "--fn", "!document.querySelector('#completions')")
	run("find", "label", "Message", "fill", "")
	waitRecovery()
	button("Retry intact")
	waitRecovered()
	run("wait", "--text", "Browser host answer 3")
	run("upload", "input[type=file]", laterUpload)
	message("browser discard input")
	waitRecovery()
	button("Discard")
	waitRecovered()
	// Freeze transcript following before the pointer targets an older row.
	run("focus", `button[aria-label="Details"]`)
	run("find", "first", `button[aria-label="Details"]`, "click")
	run("wait", "--text", "Activity details")
	capture("details")
	button("Close details")
	run("wait", "--fn", "!document.querySelector('#details-heading')")
	t.Run("queued input editing", func(t *testing.T) {
		message("hold queued editing")
		run("wait", "--fn", "!!document.querySelector('button[aria-label=Stop]')")
		run("upload", "input[type=file]", laterUpload)
		run("find", "label", "Message", "fill", "queued original")
		run("press", "Tab")
		run("wait", "--text", "1 message waits")
		capture("queued")
		// Native Alt-Up takes the browser queue into the requesting TUI view.
		transport := sessionapi.ControlTransport(socket)
		defer transport.CloseIdleConnections()
		rpc, err := sessionapi.OpenRPC(ctx, &http.Client{Transport: transport}, "http://mekugi"+sessionapi.NativeRPCPath, "")
		if err != nil {
			t.Fatal(err)
		}
		defer rpc.Close()
		subscription, err := rpc.Subscribe(ctx, "view/attach", sessionapi.RPCParams{Thread: "main"})
		if err != nil {
			t.Fatal(err)
		}
		defer subscription.Close()
		next := func() sessionapi.NativeEvent {
			t.Helper()
			event, err := subscription.Next(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var frame sessionapi.NativeEvent
			if err := json.Unmarshal(event.Data, &frame, jsonv1.FormatDurationAsNano(true)); err != nil {
				t.Fatal(err)
			}
			return frame
		}
		native := next()
		mutation := sessionapi.NativeMutation{Attachment: native.Attachment, Version: native.Frame.View.Version,
			Operation: sessionapi.NativeOperation{Composer: &sessionapi.NativeComposerInput{Intent: sessionpresentation.ComposerIntent{
				Thread: "main", Version: native.Frame.View.Version, Operation: sessionpresentation.RestoreWaiting, Work: new(native.Frame.View.Interrupt)}}}}
		body, _ := json.Marshal(mutation, jsonv1.FormatDurationAsNano(true))
		if err := rpc.Call(ctx, "view/intent", sessionapi.RPCParams{Thread: "main", Body: body}, nil); err != nil {
			t.Fatal(err)
		}
		for {
			native = next()
			if len(native.Frame.Effects) == 0 {
				continue
			}
			found := false
			for _, effect := range native.Frame.Effects {
				if effect.Restore != nil && strings.Contains(effect.Restore.Text, "queued original") {
					if len(effect.Restore.Attachments) == 0 {
						t.Fatal("TUI lost retained file contents")
					}
					found = true
				}
			}
			if found {
				break
			}
		}
		run("wait", "--fn", `!document.querySelector('button[aria-label="Edit queued messages"]') && !document.querySelector('section[aria-label="Unsent submission"]')`)
		run("find", "label", "Message", "fill", "queued original")
		run("upload", "input[type=file]", laterUpload)
		run("find", "label", "Message", "fill", "queued original")
		run("press", "Tab")
		run("wait", "--text", "1 message waits")
		button("Edit queued messages")
		run("wait", "--fn", "!!document.querySelector('textarea[aria-label=\"Message text 1\"]')")
		run("find", "label", "Message text 1", "fill", "edited queued text ")
		capture("queue-editor")
		run("click", "section[aria-label=\"Unsent submission\"] button:last-child")
		waitRecovered()
		run("wait", "--text", "1 message waits")
		body, err = os.ReadFile(ledger)
		if err != nil || strings.Contains(string(body), "edited queued text") {
			t.Fatalf("editing unexpectedly steered: %v", err)
		}
		run("find", "label", "Message", "fill", "draft stays untouched")
		button("Steer queued messages")
		run("wait", "--fn", `!document.querySelector('button[aria-label="Steer queued messages"]') && document.querySelector('#composer-text').value === 'draft stays untouched'`)
		body, err = os.ReadFile(ledger)
		if err != nil || !strings.Contains(string(body), "edited queued text") {
			t.Fatalf("explicit steer did not send: %v", err)
		}
		run("find", "label", "Message", "fill", "retry remains queued")
		run("press", "Tab")
		run("wait", "--text", "1 message waits")
		button("Stop")
		run("wait", "--fn", "!document.querySelector('button[aria-label=Stop]')")
		run("wait", "--text", "Retry keeps this message queued for the next turn.")
		message("hold queued editing again")
		run("wait", "--fn", "!!document.querySelector('button[aria-label=Stop]')")
		button("Retry intact")
		waitRecovered()
		run("wait", "--text", "1 message waits")
		body, err = os.ReadFile(ledger)
		if err != nil || strings.Contains(string(body), "retry remains queued") {
			t.Fatalf("retry unexpectedly steered: %v", err)
		}
		button("Steer queued messages")
		run("wait", "--fn", `!document.querySelector('button[aria-label="Steer queued messages"]')`)
		button("Stop")
		run("wait", "--fn", "!document.querySelector('button[aria-label=Stop]')")
	})
	message("/resume")
	run("wait", "--text", "Resume saved thread")
	run("find", "text", "Saved fixture", "click", "--exact")
	run("wait", "--text", "Saved fixture answer")
	message("/rewind")
	run("wait", "--text", "Rewind")
	closeDialog()
	t.Log("commands, completions, status, side, BTW, details, saved resume and rewind overlay consumed")
	// Detaching the browser leaves the actual server owner available natively.
	button("New thread")
	run("wait", "--text", "What should main work on?")
	transport := sessionapi.ControlTransport(socket)
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	native, err := sessionapi.OpenNative(ctx, client, "http://mekugi", "saved")
	if err != nil {
		t.Fatal(err)
	}
	event, err := native.Next()
	if err != nil {
		t.Fatal(err)
	}
	_, err = native.Send(ctx, event.Target(), sessionapi.NativeOperation{Composer: &sessionapi.NativeComposerInput{Intent: sessionpresentation.ComposerIntent{Thread: "saved", Version: event.Frame.View.Version, Operation: sessionpresentation.SubmitComposer}, Draft: composer.Draft{Text: "native continuation"}}})
	if err != nil {
		t.Fatal(err)
	}
	native.Close()
	serverCLIAwait(t, func() bool {
		data, _ := os.ReadFile(ledger)
		return strings.Contains(string(data), "native continuation")
	})
	// Resuming replaces the host thread within the existing server session.
	// Its list title remains the selected workspace, not the host picker label.
	run("click", `nav[aria-label="Threads"] a`)
	button("Session details")
	button("End thread")
	button("End")
	run("wait", "--text", "Ended")
	button("New thread")
	sessions := filepath.Join(os.Getenv("CODEX_HOME"), "sessions")
	if err := os.MkdirAll(sessions, 0700); err != nil {
		t.Fatal(err)
	}
	replayTestWrite(t, sessions, "rollout-recorded.jsonl", replayTestMeta("recorded"), replayTestRecord("event_msg", replayTestEpoch, map[string]any{"type": "task_started", "turn_id": "turn"}), replayTestItem("recorded", "turn", replayTestEpoch+1, replayTestEpoch+2, map[string]any{"type": "AgentMessage", "id": "answer", "content": []map[string]any{{"text": "Browser recorded answer"}}}), replayTestRecord("event_msg", replayTestEpoch+100, map[string]any{"type": "task_complete", "turn_id": "turn"}))
	button("More ways to start")
	run("find", "role", "menuitem", "click", "--name", "Open replay")
	run("find", "label", "Saved session ID", "fill", "recorded")
	button("Open replay")
	run("wait", "--text", "Browser recorded answer")
	button("Restart")
	button("Session details")
	button("End replay")
	button("End")
	body, err := os.ReadFile(ledger)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"first uploaded snapshot", "later uploaded snapshot", "localImage", "thread/fork", "thread/resume", "account/read", "native continuation"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("host ledger missing %q", want)
		}
	}
	if strings.Count(string(body), "thread/start ") != 1 {
		t.Fatalf("browser/native continuation started %d owners", strings.Count(string(body), "thread/start "))
	}
	for text, count := range map[string]int{"browser first input": 3, "browser later input": 2, "browser discard input": 1} {
		var original []map[string]any
		attempts := 0
		for line := range strings.SplitSeq(string(body), "\n") {
			if !strings.HasPrefix(line, "turn/start ") || !strings.Contains(line, text) {
				continue
			}
			var params struct {
				Input jsontext.Value `json:"input"`
			}
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "turn/start ")), &params); err != nil {
				t.Fatal(err)
			}
			var input []map[string]any
			if err := json.Unmarshal(params.Input, &input); err != nil {
				t.Fatal(err)
			}
			// Final submission assigns a fresh receipt identity on each attempt.
			// Normalize only that envelope ID, retaining exact frames, input order,
			// text spans and local image paths for the payload comparison.
			for _, part := range input {
				text, _ := part["text"].(string)
				if snapshot, ok := attachments.DecodeSnapshot(text); ok {
					snapshot.ID = "submission"
					part["text"] = snapshot.Encode()
				}
			}
			if attempts == 0 {
				original = input
			} else if !reflect.DeepEqual(input, original) {
				t.Errorf("%s retry changed the original attached input:\noriginal: %#v\nretry: %#v", text, original, input)
			}
			attempts++
		}
		if attempts != count {
			t.Errorf("%s: %d host submissions, want %d", text, attempts, count)
		}
	}
	t.Log("actual browser/server/connector semantic acceptance completed")
}

type webUISemanticLauncher struct {
	*serverLauncher
	failures chan error
}

func (l *webUISemanticLauncher) Run(ctx context.Context, start sessionapi.Start, api *sessionapi.Server) error {
	err := l.serverLauncher.Run(ctx, start, api)
	if err != nil {
		select {
		case l.failures <- err:
		default:
		}
	}
	return err
}

func serveWebUISemanticHost(t *testing.T) {
	log, err := os.OpenFile(os.Getenv("MEKUGI_TEST_WEBUI_SEMANTIC_LOG"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	decoder, encoder := jsontext.NewDecoder(os.Stdin), jsontext.NewEncoder(os.Stdout)
	write := func(value any) {
		t.Helper()
		if err := json.MarshalEncode(encoder, value); err != nil {
			fmt.Fprintf(log, "fixture-error: %v\n", err)
			t.Fatal(err)
		}
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	user, _ := json.Marshal([]map[string]string{{"type": "text", "text": "Saved fixture input"}})
	threads := map[string]binding.ThreadInfo{"main": {ID: "main", Cwd: cwd}, "saved": {ID: "saved", Cwd: cwd, Turns: []binding.HistoryTurn{{ID: "saved-turn", Status: "completed", Items: []binding.ThreadItem{{ID: "saved-user", Type: "userMessage", Content: user}, {ID: "saved-answer", Type: "agentMessage", Text: "Saved fixture answer"}}}}}}
	inputs, forks := 0, 0
	rejections := make(map[string]int)
	settings := map[string]any{}
	for {
		var request struct {
			ID     jsontext.Value `json:"id"`
			Method string         `json:"method"`
			Params jsontext.Value `json:"params"`
		}
		if err := json.UnmarshalDecode(decoder, &request); err != nil {
			if !errors.Is(err, io.EOF) {
				fmt.Fprintf(log, "fixture-error: %v\n", err)
				t.Fatal(err)
			}
			return
		}
		if len(request.ID) == 0 {
			continue
		}
		fmt.Fprintf(log, "%s %s\n", request.Method, request.Params)
		var params struct {
			Thread string         `json:"threadId"`
			Turn   string         `json:"turnId"`
			Input  jsontext.Value `json:"input"`
			Model  string         `json:"model"`
			Effort string         `json:"effort"`
			Tier   jsontext.Value `json:"serviceTier"`
		}
		if len(request.Params) != 0 {
			if err := json.Unmarshal(request.Params, &params); err != nil {
				fmt.Fprintf(log, "fixture-error: %v\n", err)
				t.Fatal(err)
			}
		}
		result := map[string]any{}
		var turn *binding.HistoryTurn
		var confirmed map[string]any
		switch request.Method {
		case "config/read":
			result["layers"] = []any{}
		case "thread/start":
			result["thread"] = threads["main"]
		case "thread/settings/update":
			for field, value := range map[string]string{"model": params.Model, "effort": params.Effort} {
				if value != "" {
					settings[field] = value
				}
			}
			if len(params.Tier) > 0 {
				settings["serviceTier"] = params.Tier
			}
			// Codex confirms with the thread's authoritative settings.
			confirmed = map[string]any{"method": "thread/settings/updated", "params": map[string]any{"threadId": params.Thread, "threadSettings": maps.Clone(settings)}}
		case "model/list":
			efforts := []map[string]any{{"reasoningEffort": "low"}, {"reasoningEffort": "high"}}
			result["data"] = []map[string]any{{"model": "fixture-a", "supportedReasoningEfforts": efforts}, {"model": "fixture-b", "supportedReasoningEfforts": efforts}}
		case "fuzzyFileSearch":
			result["files"] = []map[string]any{{"root": cwd, "path": "notes.md"}}
		case "thread/list":
			result["data"] = []map[string]any{{"id": "saved", "preview": "Saved fixture", "cwd": cwd, "updatedAt": 1}}
		case "thread/read", "thread/resume":
			result["thread"] = threads[params.Thread]
			result["model"] = "codex"
		case "thread/fork":
			forks++
			thread := threads[params.Thread]
			thread.ID = fmt.Sprintf("fork-%d", forks)
			threads[thread.ID] = thread
			result["thread"] = thread
		case "turn/interrupt":
			confirmed = map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": params.Thread, "turn": map[string]any{"id": params.Turn, "status": "interrupted"}}}
		case "turn/steer":
			result["turnId"] = params.Turn
			confirmed = map[string]any{"method": "item/completed", "params": map[string]any{"threadId": params.Thread, "turnId": params.Turn, "item": map[string]any{"id": "queued-edit", "type": "userMessage", "content": params.Input}}}
		case "turn/start":
			limit := 0
			key := ""
			for text, n := range map[string]int{"browser first input": 2, "browser later input": 1, "browser discard input": 1} {
				if strings.Contains(string(params.Input), text) {
					limit = n
					key = text
				}
			}
			if rejections[key] < limit {
				rejections[key]++
				write(map[string]any{"id": request.ID, "error": map[string]any{"code": -1, "message": fmt.Sprintf("Fixture rejection %d", rejections[key])}})
				continue
			}
			inputs++
			thread := threads[params.Thread]
			id := fmt.Sprintf("turn-%d", inputs)
			content := params.Input
			history := binding.HistoryTurn{ID: id, Status: "completed", Items: []binding.ThreadItem{{ID: fmt.Sprintf("user-%d", inputs), Type: "userMessage", Content: content}, {ID: fmt.Sprintf("answer-%d", inputs), Type: "agentMessage", Text: fmt.Sprintf("Browser host answer %d", inputs)}}}
			thread.Turns = append(thread.Turns, history)
			threads[thread.ID] = thread
			turn = &history
			result["turn"] = map[string]any{"id": id}
		}
		write(map[string]any{"id": request.ID, "result": result})
		if confirmed != nil {
			write(confirmed)
		}
		if turn != nil {
			write(map[string]any{"method": "turn/started", "params": map[string]any{"threadId": params.Thread, "turn": map[string]any{"id": turn.ID}}})
			if strings.Contains(string(turn.Items[0].Content), "hold queued editing") {
				write(map[string]any{"method": "item/completed", "params": map[string]any{"threadId": params.Thread, "turnId": turn.ID, "item": turn.Items[0]}})
				continue
			}
			for _, item := range turn.Items {
				write(map[string]any{"method": "item/completed", "params": map[string]any{"threadId": params.Thread, "turnId": turn.ID, "item": item}})
			}
			write(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": params.Thread, "turn": map[string]any{"id": turn.ID, "status": "completed"}}})
		}
	}
}
