package launch

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/commandargs"
)

func TestNativeServerSavedResume(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_SAVED_HOST") == "1" {
		serveSavedResumeHost(t)
		os.Exit(0)
	}
	fixtureTemp := os.TempDir()
	for _, key := range []string{"XDG_CONFIG_HOME", "XDG_STATE_HOME", "CODEX_HOME", "MEKUGI_RUNTIME_DIR", "TMPDIR"} {
		t.Setenv(key, t.TempDir())
	}
	for _, key := range []string{"XAI_API_KEY", "OPENCODE_API_KEY", "OPENCODE_GO_API_KEY", "OPENCODE_ZEN_API_KEY"} {
		t.Setenv(key, "")
	}
	writeFeatureConfig(t, "shell_guard = false")
	hostState := filepath.Join(t.TempDir(), "saved-thread.json")
	hostLedger := filepath.Join(t.TempDir(), "requests.jsonl")
	t.Cleanup(func() {
		if t.Failed() {
			ledger, _ := os.ReadFile(hostLedger)
			t.Logf("host requests:\n%s", ledger)
		}
	})
	t.Setenv("MEKUGI_TEST_SAVED_STATE", hostState)
	t.Setenv("MEKUGI_TEST_SAVED_LEDGER", hostLedger)
	bin := t.TempDir()
	script := "#!/bin/sh\nif [ \"$1\" = debug ]; then exit 1; fi\nMEKUGI_TEST_SAVED_HOST=1; export MEKUGI_TEST_SAVED_HOST\nexec " + commandargs.Quote(os.Args[0]) + " -test.run=^TestNativeServerSavedResume$ -- \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	socketDirectory, err := os.MkdirTemp(fixtureTemp, "mekugi-saved-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(socketDirectory)
	socket := filepath.Join(socketDirectory, "s")
	startServer := func() func() {
		t.Helper()
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan int, 1)
		var diagnostic bytes.Buffer
		go func() { done <- RunServer(ctx, []string{"--socket", socket}, &diagnostic) }()
		stopped := false
		stop := func() {
			t.Helper()
			if stopped {
				return
			}
			stopped = true
			cancel()
			select {
			case code := <-done:
				if code != 0 {
					t.Errorf("server exit %d: %s", code, &diagnostic)
				}
			case <-time.After(10 * time.Second):
				t.Error("server did not join saved-session host")
			}
		}
		t.Cleanup(stop)
		serverCLIAwait(t, func() bool { _, err := os.Stat(socket); return err == nil })
		return stop
	}
	stop := startServer()
	transport := sessionapi.ControlTransport(socket)
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	open := func() *sessionapi.RPCClient {
		t.Helper()
		rpc, err := sessionapi.OpenRPC(t.Context(), client, "http://mekugi"+sessionapi.RPCPath, "")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { rpc.Close() })
		return rpc
	}
	rpc := open()
	workspace := t.TempDir()
	create := func(start sessionapi.Start) {
		t.Helper()
		data := serverRPCCall(t, rpc, "session/start", "", start)
		var created struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(data, &created); err != nil || created.ID != "main" {
			t.Fatalf("created identity %q: %v", created.ID, err)
		}
	}
	create(sessionapi.Start{Workspace: workspace, Model: "codex", Text: "saved first input"})
	events := serverRPCSubscribe(t, rpc, "main")
	serverCLIEvent(t, events, "", "saved host answer 1")
	events.Close()
	// The fake host writes its actual thread response before publishing events.
	// A fresh host process must load this file, rather than inventing resume data.
	savedBytes, err := os.ReadFile(hostState)
	if err != nil {
		t.Fatal(err)
	}
	var saved binding.ThreadInfo
	if err := json.Unmarshal(savedBytes, &saved); err != nil || saved.ID != "main" || len(saved.Turns) != 1 || saved.Turns[0].Status != "completed" {
		t.Fatalf("persisted host history: %s: %v", savedBytes, err)
	}
	latest := func() *sessionapi.NativeConnection {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		t.Cleanup(cancel)
		connection, err := sessionapi.StartNative(ctx, client, "http://mekugi", sessionapi.NativeStart{Workspace: workspace, Args: []string{"resume", "--last"}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { connection.Close() })
		return connection
	}
	// Both cancellation and successful attachment must retire the lookup-only
	// host without touching the execution owner or issuing another host resume.
	for attempt, attach := range []bool{false, true} {
		source := latest()
		var event sessionapi.NativeEvent
		for {
			event, err = source.Next()
			if err != nil {
				t.Fatalf("latest lookup did not reach its native view: %v", err)
			}
			if len(event.Frame.Effects) != 0 {
				break
			}
		}
		if event.Startup == "" || event.Frame.View.Main.ID != "" || len(event.Frame.Effects) != 1 || event.Frame.Effects[0].Reattach == nil || *event.Frame.Effects[0].Reattach != "main" {
			t.Fatalf("latest lookup did not request existing-owner attachment: %+v", event)
		}
		if attach {
			u, _ := newAppServerTestUI()
			c := &nativeClient{ctx: t.Context(), connection: source, renderer: nativeClientRenderer{terminal: u.shell, views: u.terminalViews}}
			c.accept(event)
			done := make(chan nativeClientReattached, 1)
			c.attachDestination(t.Context(), done)
			result := <-done
			destination := c.applyReattachment(result)
			if result.err != nil || destination == nil || result.event.Frame.View.Main.ID != "main" {
				t.Fatalf("latest owner attachment failed: %v", result.err)
			}
			destination.connection.Close()
			c.connection.Close()
			c.workers.Wait()
		} else {
			source.Close()
		}
		serverCLIAwait(t, func() bool {
			ledger, _ := os.ReadFile(hostLedger)
			return strings.Count(string(ledger), "host/closed\n") == attempt+1
		})
		ledger, err := os.ReadFile(hostLedger)
		if err != nil || strings.Contains(string(ledger), "thread/resume ") || strings.Count(string(ledger), "thread/start ") != 1 {
			t.Fatalf("latest attachment duplicated host execution: %v\n%s", err, ledger)
		}
		serverRPCCall(t, rpc, "session/read", "main", nil)
	}
	serverRPCCall(t, rpc, "session/end", "main", nil)
	serverCLIAwait(t, func() bool {
		err := rpc.Call(t.Context(), "session/read", sessionapi.RPCParams{Thread: "main"}, nil)
		failure, ok := errors.AsType[*sessionapi.RPCError](err)
		return ok && failure.Data != nil && failure.Data.Status == 404
	})
	rpc.Close()
	stop()
	serverCLIResourcesEmpty(t)
	transport.CloseIdleConnections()
	startServer()
	rpc = open()
	create(sessionapi.Start{Workspace: workspace, Model: "codex", Resume: "main"})
	events = serverRPCSubscribe(t, rpc, "main")
	serverCLIEvent(t, events, "snapshot", "saved host answer 1")
	ledger, err := os.ReadFile(hostLedger)
	if err != nil {
		t.Fatal(err)
	}
	// At this point resumed publications have reached the consumer. Restoration
	// must not submit the old prompt again or create a replacement host thread.
	if strings.Count(string(ledger), "thread/start ") != 1 || strings.Count(string(ledger), "thread/resume ") != 1 || strings.Count(string(ledger), "turn/start ") != 1 || strings.Contains(string(ledger), "turn/steer ") {
		t.Fatalf("resume replayed execution:\n%s", ledger)
	}
	serverRPCCall(t, rpc, "session/input", "main", sessionapi.InputRequest{Text: "after saved resume"})
	serverCLIEvent(t, events, "patch", "saved host answer 2")
	events.Close()
	ledger, err = os.ReadFile(hostLedger)
	if err != nil {
		t.Fatal(err)
	}
	var submitted []string
	for line := range strings.SplitSeq(string(ledger), "\n") {
		method, params, _ := strings.Cut(line, " ")
		if method != "turn/start" {
			continue
		}
		var turn struct {
			Thread string `json:"threadId"`
			Input  []struct {
				Text string `json:"text"`
			} `json:"input"`
		}
		if err := json.Unmarshal([]byte(params), &turn); err != nil {
			t.Fatal(err)
		}
		if turn.Thread != "main" || len(turn.Input) != 1 {
			t.Fatalf("resumed host submission: %s", line)
		}
		submitted = append(submitted, turn.Input[0].Text)
	}
	if len(submitted) != 2 || submitted[0] != "saved first input" || submitted[1] != "after saved resume" {
		t.Fatalf("host submitted inputs: %q", submitted)
	}
	serverRPCCall(t, rpc, "session/end", "main", nil)
	serverCLIAwait(t, func() bool {
		err := rpc.Call(t.Context(), "session/read", sessionapi.RPCParams{Thread: "main"}, nil)
		failure, ok := errors.AsType[*sessionapi.RPCError](err)
		return ok && failure.Data != nil && failure.Data.Status == 404
	})
	resumed := latest()
	for {
		event, err := resumed.Next()
		if err != nil {
			t.Fatalf("saved latest resume failed: %v", err)
		}
		if event.Frame.View.Main.ID == "main" {
			break
		}
	}
	ledger, err = os.ReadFile(hostLedger)
	if err != nil || strings.Count(string(ledger), "thread/resume ") != 2 || strings.Count(string(ledger), "thread/start ") != 1 || strings.Count(string(ledger), "turn/start ") != 2 {
		t.Fatalf("saved latest resume did not preserve host execution: %v\n%s", err, ledger)
	}
}

// This process is the host fixture, not a sessionapi peer. Thread history is
// persisted on first execution and reloaded by the second host after restart.
func serveSavedResumeHost(t *testing.T) {
	t.Helper()
	ledger, err := os.OpenFile(os.Getenv("MEKUGI_TEST_SAVED_LEDGER"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	lookedUpLatest := false
	defer func() {
		if lookedUpLatest {
			_, _ = fmt.Fprintln(ledger, "host/closed")
		}
	}()
	decoder, encoder := jsontext.NewDecoder(os.Stdin), jsontext.NewEncoder(os.Stdout)
	write := func(value any) {
		if err := json.MarshalEncode(encoder, value); err != nil {
			t.Fatal(err)
		}
	}
	var thread binding.ThreadInfo
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
		if _, err := fmt.Fprintf(ledger, "%s %s\n", request.Method, request.Params); err != nil {
			t.Fatal(err)
		}
		result := map[string]any{}
		var turn *binding.HistoryTurn
		switch request.Method {
		case "config/read":
			result["layers"] = []any{}
		case "thread/start":
			cwd, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			thread = binding.ThreadInfo{ID: "main", Cwd: cwd}
			result["thread"] = thread
		case "thread/list":
			var params struct {
				Limit int `json:"limit"`
			}
			if err := json.Unmarshal(request.Params, &params); err != nil {
				t.Fatal(err)
			}
			if params.Limit == 25 {
				lookedUpLatest = true
				body, err := os.ReadFile(os.Getenv("MEKUGI_TEST_SAVED_STATE"))
				if err != nil {
					t.Fatal(err)
				}
				var saved binding.ThreadInfo
				if err := json.Unmarshal(body, &saved); err != nil {
					t.Fatal(err)
				}
				result["data"] = []binding.ThreadInfo{{ID: "stock-only", Cwd: saved.Cwd}, saved}
			}
		case "thread/read", "thread/resume":
			body, err := os.ReadFile(os.Getenv("MEKUGI_TEST_SAVED_STATE"))
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(body, &thread); err != nil {
				t.Fatal(err)
			}
			var params struct {
				Thread string `json:"threadId"`
			}
			if err := json.Unmarshal(request.Params, &params); err != nil || params.Thread != thread.ID {
				t.Fatalf("resume identity %q: %v", params.Thread, err)
			}
			result["thread"] = thread
			result["model"] = "codex"
		case "turn/start":
			// The host records text exactly as admitted, without a second API input path.
			var input struct {
				Input []struct {
					Text string `json:"text"`
				} `json:"input"`
			}
			if err := json.Unmarshal(request.Params, &input); err != nil {
				t.Fatal(err)
			}
			if len(input.Input) != 1 {
				t.Fatal("expected one host text input")
			}
			content, err := json.Marshal([]map[string]string{{"type": "text", "text": input.Input[0].Text}})
			if err != nil {
				t.Fatal(err)
			}
			number := len(thread.Turns) + 1
			history := binding.HistoryTurn{ID: fmt.Sprintf("saved-turn-%d", number), Status: "completed", Items: []binding.ThreadItem{
				{ID: fmt.Sprintf("user-%d", number), Type: "userMessage", Content: content},
				{ID: fmt.Sprintf("answer-%d", number), Type: "agentMessage", Text: fmt.Sprintf("saved host answer %d", number)},
			}}
			thread.Turns = append(thread.Turns, history)
			body, err := json.Marshal(thread)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(os.Getenv("MEKUGI_TEST_SAVED_STATE"), body, 0600); err != nil {
				t.Fatal(err)
			}
			turn = &history
			result["turn"] = map[string]any{"id": history.ID}
		}
		write(map[string]any{"id": request.ID, "result": result})
		if turn != nil {
			write(map[string]any{"method": "turn/started", "params": map[string]any{"threadId": thread.ID, "turn": map[string]any{"id": turn.ID}}})
			write(map[string]any{"method": "item/completed", "params": map[string]any{"threadId": thread.ID, "turnId": turn.ID, "item": turn.Items[1]}})
			write(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": thread.ID, "turn": map[string]any{"id": turn.ID, "status": "completed"}}})
		}
	}
}
