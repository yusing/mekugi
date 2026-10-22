package launch

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/libraries/commandargs"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

// This exercises the actual invocation acquisition, host pipes and owner loop.
// The only subprocess fixture substitutes Codex, never a hosted Mekugi terminal.
func TestServerSessionNoTerminal(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_SERVER_RUNTIME_HOST") == "1" {
		serveAPIRuntimeHost(t)
		os.Exit(0)
	}
	workspace := installAPIRuntimeHost(t)
	ledgerPath := filepath.Join(t.TempDir(), "host.jsonl")
	t.Setenv("MEKUGI_TEST_RUNTIME_LEDGER", ledgerPath)
	ctx, cancel := context.WithCancel(t.Context())
	preparing, releasePreparation := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-releasePreparation:
		default:
			close(releasePreparation)
		}
	}()
	launcher := &nativeReattachLauncher{prepare: func(ctx context.Context, workspace string, input sessionapi.InputRequest) (*sessionapi.PreparedInput, error) {
		if input.NativeDraft != nil && input.NativeDraft.Text == "@message.txt" {
			close(preparing)
			select {
			case <-releasePreparation:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return prepareAPIInput(ctx, workspace, input)
	}}
	done := make(chan error, 1)
	launcher.run = func(ctx context.Context, start sessionapi.Start, api *sessionapi.Server) error {
		_, err := runServerSession(ctx, []string{"--shell-guard=false"}, []string{"--yolo"}, api, start.Workspace)
		done <- err
		return err
	}
	hub := sessionapi.NewHub(ctx, launcher)
	defer hub.Close()
	joined := false
	defer func() {
		cancel()
		if !joined {
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Error("runtime did not join after cancellation")
			}
		}
	}()
	server := httptest.NewServer(sessionapi.Versioned(hub.Handler()))
	defer server.Close()
	client := &http.Client{Timeout: 10 * time.Second}
	rpc, err := sessionapi.OpenRPC(t.Context(), client, server.URL+sessionapi.RPCPath, "")
	if err != nil {
		t.Fatal(err)
	}
	defer rpc.Close()
	var started struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(serverRPCCall(t, rpc, "session/start", "", sessionapi.Start{Workspace: workspace, Model: "fixture"}), &started); err != nil || started.ID != "main" {
		t.Fatalf("runtime start: %+v, %v", started, err)
	}
	events := serverRPCSubscribe(t, rpc, started.ID)
	await := func(predicate func([]byte) bool) {
		t.Helper()
		wait, stop := context.WithTimeout(t.Context(), 10*time.Second)
		defer stop()
		for {
			event, err := events.Next(wait)
			if err != nil {
				t.Fatalf("publication ended before expected state: %v", err)
			}
			if predicate(event.Data) {
				return
			}
		}
	}
	await(func(data []byte) bool {
		var state struct {
			Session sessionapi.Session `json:"session"`
			Input   *sessionapi.Input  `json:"input"`
		}
		return json.Unmarshal(data, &state) == nil && state.Session.Thread == "main" && state.Session.Workspace == workspace && state.Input != nil && state.Input.Reason == ""
	})
	native, err := sessionapi.OpenNative(t.Context(), client, server.URL, started.ID)
	if err != nil {
		t.Fatal(err)
	}
	event, err := native.Next()
	if err != nil || event.Frame.View.Main.ID != "main" || event.Frame.View.Info.Workspace != workspace || event.Frame.View.Version.Attachment == 0 {
		t.Fatalf("native owner publication: %+v, %v", event, err)
	}
	defer native.Close()
	post := func(text string) {
		t.Helper()
		serverRPCCall(t, rpc, "session/input", started.ID, sessionapi.InputRequest{Text: text})
	}
	if err := os.WriteFile(filepath.Join(workspace, "message.txt"), []byte("captured input"), 0600); err != nil {
		t.Fatal(err)
	}
	// The captured frame is already older before transport preparation starts.
	post("first remote input before native preparation")
	await(func(data []byte) bool { return strings.Contains(string(data), `"running":true`) })
	advance := func(previous sessionpresentation.Version) sessionapi.NativeEvent {
		t.Helper()
		for {
			advanced, err := native.Next()
			if err != nil {
				t.Fatal(err)
			}
			if advanced.Frame.View.Version != previous {
				return advanced
			}
		}
	}
	beforePreparation := advance(event.Frame.View.Version)
	submitted := make(chan error, 1)
	go func() {
		_, err := native.Send(t.Context(), event.Target(), sessionapi.NativeOperation{Composer: &sessionapi.NativeComposerInput{Intent: sessionpresentation.ComposerIntent{Version: event.Frame.View.Version, Thread: "main", Operation: sessionpresentation.SubmitComposer}, Draft: composer.Draft{Text: "@message.txt", Files: []composer.Token{{Start: 0, End: len("@message.txt"), Path: "message.txt"}}}}})
		submitted <- err
	}()
	select {
	case <-preparing:
	case err := <-submitted:
		t.Fatalf("old frame rejected before preparation: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("native preparation did not start")
	}
	post("second remote input while native preparation is blocked")
	advance(beforePreparation.Frame.View.Version)
	events.Close()
	close(releasePreparation)
	select {
	case err := <-submitted:
		if err != nil {
			t.Fatalf("display progress invalidated prepared input: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("prepared input admission stalled")
	}
	// Queueing uses the same old frame, but must wait for turn completion.
	if _, err := native.Send(t.Context(), event.Target(), sessionapi.NativeOperation{Composer: &sessionapi.NativeComposerInput{Intent: sessionpresentation.ComposerIntent{Version: event.Frame.View.Version, Thread: "main", Operation: sessionpresentation.QueueComposer}, Draft: composer.Draft{Text: "queued native input"}}}); err != nil {
		t.Fatalf("display progress invalidated queued input: %v", err)
	}
	var queuedEvent sessionapi.NativeEvent
	for {
		queued, err := native.Next()
		if err != nil {
			t.Fatal(err)
		}
		if len(queued.Frame.View.Pending.Queued) == 1 {
			queuedEvent = queued
			break
		}
	}
	ledger, err := os.ReadFile(ledgerPath)
	if err != nil || strings.Contains(string(ledger), "queued native input") {
		t.Fatalf("queued input reached host before completion: %s, %v", ledger, err)
	}
	u, _ := newAppServerTestUI()
	local := nativeTestPromptClient(u)
	local.accept(queuedEvent)
	local.editor("main").InsertText("local draft")
	local.refresh(time.Now())
	local.dispatch(terminalsession.Input{Kind: terminalsession.KeyInput, Text: "\x1b[1;3A"})
	var restore *nativeClientCall
	for _, call := range local.calls {
		if call.operation.Composer != nil && call.operation.Composer.Intent.Operation == sessionpresentation.RestoreWaiting {
			restore = call
		}
	}
	if restore == nil {
		t.Fatal("Alt-Up did not request queued-input restoration")
	}
	post("progress before queued-input editing")
	advance(queuedEvent.Frame.View.Version)
	if _, err := native.Send(t.Context(), restore.target, restore.operation); err != nil {
		t.Fatalf("progress prevented Alt-Up queued-input editing: %v", err)
	}
	for local.editor("main").Text != "queued native input\nlocal draft" {
		next, err := native.Next()
		if err != nil {
			t.Fatal(err)
		}
		local.accept(next)
	}
	if len(local.event.Frame.View.Pending.Queued) != 0 {
		t.Fatal("restored input remained queued")
	}
	// Requeue the edited draft, preserving the original host-delivery assertions.
	if _, err := native.Send(t.Context(), local.event.Target(), sessionapi.NativeOperation{Composer: &sessionapi.NativeComposerInput{Intent: sessionpresentation.ComposerIntent{Version: local.event.Frame.View.Version, Thread: "main", Operation: sessionpresentation.QueueComposer}, Draft: local.editor("main").Draft.Clone()}}); err != nil {
		t.Fatal(err)
	}
	post("release queued input")
	serverCLIAwait(t, func() bool {
		ledger, _ := os.ReadFile(ledgerPath)
		return strings.Contains(string(ledger), "queued native input")
	})
	var running sessionapi.NativeEvent
	for {
		running, err = native.Next()
		if err != nil {
			t.Fatal(err)
		}
		if running.Frame.View.Status.Session.Running && len(running.Frame.View.Pending.Queued) == 0 {
			break
		}
	}
	if _, err := native.Send(t.Context(), restore.target, restore.operation); err != nil {
		t.Fatalf("completed original restoration did not settle: %v", err)
	}
	for {
		next, err := native.Next()
		if err != nil {
			t.Fatal(err)
		}
		if next.Frame.View.Status.Feedback.Text == "Original work already ended." {
			if next.Frame.View.Interrupt != running.Frame.View.Interrupt || len(next.Frame.Effects) != len(running.Frame.Effects) {
				t.Fatal("completed original restoration changed successor work or editor consequences")
			}
			break
		}
	}
	native.Close()
	post("follow-up after disconnect")
	serverCLIAwait(t, func() bool {
		ledger, _ := os.ReadFile(ledgerPath)
		return strings.Contains(string(ledger), "follow-up after disconnect")
	})
	cancel()
	select {
	case err := <-done:
		joined = true
		if err == nil {
			t.Fatal("server stop did not cancel the live host")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("server stop did not join its host and invocation resources")
	}
	ledger, err = os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	wants := []struct{ method, text string }{
		{"turn/start", "first remote input before native preparation"},
		{"turn/steer", "second remote input while native preparation is blocked"},
		{"turn/steer", "captured input"},
		{"turn/steer", "progress before queued-input editing"},
		{"turn/steer", "release queued input"},
		{"turn/start", "queued native input\nlocal draft"},
		{"turn/steer", "follow-up after disconnect"},
	}
	lines := strings.Split(strings.TrimSpace(string(ledger)), "\n")
	if len(lines) != len(wants) {
		t.Fatalf("unexpected host input count: %s", ledger)
	}
	for i, line := range lines {
		var request struct {
			Method string `json:"method"`
			Params struct {
				Thread string `json:"threadId"`
				Input  []struct {
					Text string `json:"text"`
				} `json:"input"`
			} `json:"params"`
		}
		if err := json.Unmarshal([]byte(line), &request); err != nil {
			t.Fatal(err)
		}
		var text strings.Builder
		for _, input := range request.Params.Input {
			text.WriteString(input.Text)
		}
		if request.Method != wants[i].method || request.Params.Thread != "main" || !strings.Contains(text.String(), wants[i].text) {
			t.Fatalf("host input %d lost method, target or captured draft: %s", i, line)
		}
	}
	for _, key := range []string{"MEKUGI_RUNTIME_DIR", "TMPDIR"} {
		entries, err := os.ReadDir(os.Getenv(key))
		if err != nil || len(entries) != 0 {
			t.Errorf("owned %s resources survived shutdown: %v, %v", key, entries, err)
		}
	}
}

func serveAPIRuntimeHost(t *testing.T) {
	t.Helper()
	decoder, encoder := jsontext.NewDecoder(os.Stdin), jsontext.NewEncoder(os.Stdout)
	write := func(value any) {
		t.Helper()
		if err := json.MarshalEncode(encoder, value); err != nil {
			t.Fatal(err)
		}
	}
	turn, starts := "", 0
	for {
		var request struct {
			ID     jsontext.Value `json:"id"`
			Method string         `json:"method"`
			Params jsontext.Value `json:"params"`
		}
		if err := json.UnmarshalDecode(decoder, &request); err != nil {
			if !errors.Is(err, io.EOF) {
				t.Fatal(err)
			}
			return
		}
		if len(request.ID) == 0 {
			continue
		}
		if path := os.Getenv("MEKUGI_TEST_RUNTIME_LEDGER"); path != "" && (request.Method == "turn/start" || request.Method == "turn/steer") {
			file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				t.Fatal(err)
			}
			err = json.MarshalWrite(file, request)
			if err == nil {
				_, err = file.WriteString("\n")
			}
			file.Close()
			if err != nil {
				t.Fatal(err)
			}
		}
		result := map[string]any{}
		switch request.Method {
		case "config/read":
			result["layers"] = []any{}
		case "thread/start":
			workspace, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			result["thread"] = map[string]any{"id": "main", "cwd": workspace}
		case "turn/start":
			starts++
			turn = "turn-" + strconv.Itoa(starts)
			result["turn"] = map[string]any{"id": turn}
		case "turn/steer":
			result["turnId"] = turn
		case "thread/archive":
			write(map[string]any{"method": "thread/tokenUsage/updated", "params": map[string]any{"threadId": "main", "tokenUsage": map[string]any{"total": map[string]any{"inputTokens": 12, "outputTokens": 5, "cachedInputTokens": 2, "totalTokens": 17}}}})
		}
		write(map[string]any{"id": request.ID, "result": result})
		if request.Method == "turn/steer" && strings.Contains(string(request.Params), "release queued input") {
			write(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "main", "turn": map[string]any{"id": turn, "status": "completed"}}})
		}
		if request.Method == "turn/start" {
			write(map[string]any{"method": "turn/started", "params": map[string]any{"threadId": "main", "turn": map[string]any{"id": turn}}})
		}
	}
}

func installAPIRuntimeHost(t *testing.T) string {
	t.Helper()
	for _, key := range []string{"XDG_CONFIG_HOME", "XDG_STATE_HOME", "CODEX_HOME", "MEKUGI_RUNTIME_DIR", "TMPDIR"} {
		t.Setenv(key, t.TempDir())
	}
	workspace, executableDirectory := t.TempDir(), t.TempDir()
	script := "#!/bin/sh\nMEKUGI_TEST_SERVER_RUNTIME_HOST=1; export MEKUGI_TEST_SERVER_RUNTIME_HOST\nexec " + commandargs.Quote(os.Args[0]) + " -test.run=^TestServerSessionNoTerminal$ -- \"$@\"\n"
	if err := os.WriteFile(filepath.Join(executableDirectory, "codex"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", executableDirectory+string(os.PathListSeparator)+os.Getenv("PATH"))
	return workspace
}

type nativeRuntimeTestLauncher struct {
	serverLauncher
	joined chan error
}

func (l *nativeRuntimeTestLauncher) Run(ctx context.Context, start sessionapi.Start, api *sessionapi.Server) error {
	err := l.serverLauncher.Run(ctx, start, api)
	l.joined <- err
	return err
}

func TestNativeStartupChooserDisconnectJoinsUnconfirmedHost(t *testing.T) {
	workspace := installAPIRuntimeHost(t)
	launcher := &nativeRuntimeTestLauncher{joined: make(chan error, 1)}
	hub := sessionapi.NewHub(t.Context(), launcher)
	defer hub.Close()
	server := httptest.NewServer(sessionapi.Versioned(hub.Handler()))
	defer server.Close()
	client := &http.Client{Timeout: 10 * time.Second}
	connection, err := sessionapi.StartNative(t.Context(), client, server.URL, sessionapi.NativeStart{Workspace: workspace, RouterArgs: []string{"--shell-guard=false"}, Args: []string{"resume", "--yolo"}})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	for {
		event, err := connection.Next()
		if err != nil {
			t.Fatalf("startup chooser: %v", err)
		}
		if event.Frame.View.Main.ID != "" {
			t.Fatal("chooser created a thread before selection")
		}
		if chooser := event.Frame.View.Resume; chooser != nil && !chooser.Loading {
			break
		}
	}
	connection.Close()
	select {
	case err := <-launcher.joined:
		var outcome *sessionapi.NativeExit
		if !errors.As(err, &outcome) || outcome.Code != 0 {
			t.Fatalf("unconfirmed startup did not join cleanly: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("detached pre-thread chooser left its host running")
	}
}

func TestNativeArchiveJoinsOwningRuntime(t *testing.T) {
	workspace := installAPIRuntimeHost(t)
	launcher := &nativeRuntimeTestLauncher{joined: make(chan error, 1)}
	hub := sessionapi.NewHub(t.Context(), launcher)
	defer hub.Close()
	server := httptest.NewServer(sessionapi.Versioned(hub.Handler()))
	defer server.Close()
	client := &http.Client{Timeout: 10 * time.Second}
	connection, err := sessionapi.StartNative(t.Context(), client, server.URL, sessionapi.NativeStart{Workspace: workspace, RouterArgs: []string{"--shell-guard=false"}, Args: []string{"--yolo"}})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	var event sessionapi.NativeEvent
	for event.Frame.View.Main.ID == "" {
		event, err = connection.Next()
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := connection.Send(t.Context(), event.Target(), sessionapi.NativeOperation{Command: new("/archive")}); err != nil {
		t.Fatal(err)
	}
	for event.Frame.View.Menu == nil {
		event, err = connection.Next()
		if err != nil {
			t.Fatal(err)
		}
	}
	menu := event.Frame.View.Menu
	intent := sessionpresentation.MenuAction{Version: event.Frame.View.Version, Menu: menu.ID, Kind: sessionpresentation.MenuAccept}
	for _, choice := range menu.Choices {
		if choice.ID == archiveConfirmChoice {
			intent.Choice = choice.Identity()
		}
	}
	if _, err := connection.Send(t.Context(), event.Target(), sessionapi.NativeOperation{Menu: &intent}); err != nil {
		t.Fatal(err)
	}
	finalFrame := false
	for {
		event, err := connection.Next()
		if err == nil {
			finalFrame = event.Frame.Facts.Archived && event.Frame.Facts.Usage.TotalTokens == 17
			continue
		}
		var outcome *sessionapi.NativeExit
		if !errors.As(err, &outcome) || outcome.Code != 0 {
			t.Fatalf("archive owner outcome: %v", err)
		}
		break
	}
	if !finalFrame {
		t.Fatal("joined exit arrived before final archive/usage facts")
	}
	select {
	case <-launcher.joined:
	default:
		t.Fatal("archive closed a view before joining its runtime")
	}
}
