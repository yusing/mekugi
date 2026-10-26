package launch

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/libraries/commandargs"
)

func TestNativeProcessControlPublication(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_PROCESS_HOST") == "1" {
		serveNativeProcessHost(t)
		os.Exit(0)
	}
	workspace := installAPIRuntimeHost(t)
	bin := t.TempDir()
	script := "#!/bin/sh\nif [ \"$1\" = debug ]; then exit 1; fi\nMEKUGI_TEST_PROCESS_HOST=1; export MEKUGI_TEST_PROCESS_HOST\nexec " + commandargs.Quote(os.Args[0]) + " -test.run=^TestNativeProcessControlPublication$ -- \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	hub := sessionapi.NewHub(ctx, &serverLauncher{})
	defer hub.Close()
	server := httptest.NewServer(sessionapi.Versioned(hub.Handler()))
	defer server.Close()
	connection, err := sessionapi.StartNative(ctx, server.Client(), server.URL, sessionapi.NativeStart{Workspace: workspace, RouterArgs: []string{"--shell-guard=false"}, Args: []string{"--yolo"}})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	var event sessionapi.NativeEvent
	for len(event.Frame.Processes) == 0 {
		event, err = connection.Next()
		if err != nil {
			t.Fatal(err)
		}
	}
	process := event.Frame.Processes[0]
	if process.Thread != "main" || process.Turn != "turn" || process.Item != "command" || process.ID != "42" || process.Command != "sleep 30" {
		t.Fatalf("published process lost host identity: %+v", process)
	}
	u, _ := newAppServerTestUI()
	c := nativeTestPromptClient(u)
	c.accept(event)
	c.calls = nil
	c.editor("main").InsertText("/ps")
	_, target := c.selectedEditor()
	c.composer(terminalsession.ComposerInput{Command: sessionpresentation.ComposerIntent{Version: event.Frame.View.Version, Thread: "main", Target: target, Operation: sessionpresentation.SubmitComposer}})
	if len(c.calls) != 1 || c.calls[0].operation.Composer == nil {
		t.Fatal("/ps did not use ordinary native command submission")
	}
	if _, err := connection.Send(ctx, c.calls[0].target, c.calls[0].operation); err != nil {
		t.Fatal(err)
	}
	for event.Frame.Details == nil {
		event, err = connection.Next()
		if err != nil {
			t.Fatal(err)
		}
	}
	c.accept(event)
	c.calls = nil
	dialog := c.renderer.terminal.Dialog
	stop := dialog.Key("s")
	if stop.Process == nil || stop.Process.Thread != process.Thread || stop.Process.Turn != process.Turn || stop.Process.Item != process.Item || stop.Process.ID != process.ID {
		t.Fatalf("selected process lost its captured identity: %+v", stop)
	}
	stale := process
	stale.Item = "replaced"
	if _, err := connection.Send(ctx, event.Target(), sessionapi.NativeOperation{StopProcess: &stale}); err == nil {
		t.Fatal("stale process reached host dispatch")
	}
	c.dialog(stop)
	if len(c.calls) != 2 || c.calls[0].operation.CloseDetails == nil || c.calls[1].operation.StopProcess == nil {
		t.Fatal("Stop did not dismiss output and submit its captured process")
	}
	for _, call := range c.calls {
		if _, err := connection.Send(ctx, call.target, call.operation); err != nil {
			t.Fatal(err)
		}
	}
	for event.Frame.View.Status.Feedback.Text != "Could not stop process: fixture refusal" || event.Frame.Details != nil {
		event, err = connection.Next()
		if err != nil {
			t.Fatal(err)
		}
	}
	c.accept(event)
	c.renderer.terminal.Width, c.renderer.terminal.Height = 100, 24
	var screen bytes.Buffer
	if _, err := c.renderer.render(ctx, &screen, time.Now(), "test"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(screen.String(), "Could not stop process: fixture refusal") || len(event.Frame.Processes) != 1 {
		t.Fatal("host refusal was hidden or removed the running process")
	}
	if _, err := connection.Send(ctx, event.Target(), sessionapi.NativeOperation{StopProcess: &process}); err != nil {
		t.Fatal(err)
	}
	for len(event.Frame.Processes) != 0 {
		event, err = connection.Next()
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := connection.Send(ctx, event.Target(), sessionapi.NativeOperation{StopProcess: &process}); err == nil {
		t.Fatal("completed process retained Stop capability")
	}
	if event.Frame.Details != nil {
		t.Fatal("process dialog hid stop results")
	}
}

func serveNativeProcessHost(t *testing.T) {
	t.Helper()
	decoder, encoder := jsontext.NewDecoder(os.Stdin), jsontext.NewEncoder(os.Stdout)
	write := func(value any) {
		t.Helper()
		if err := json.MarshalEncode(encoder, value); err != nil {
			t.Fatal(err)
		}
	}
	stops := 0
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
		result := map[string]any{}
		switch request.Method {
		case "config/read":
			result["layers"] = []any{}
		case "thread/start":
			cwd, _ := os.Getwd()
			result["thread"] = map[string]any{"id": "main", "cwd": cwd}
		case "thread/backgroundTerminals/terminate":
			var params struct {
				Thread  string `json:"threadId"`
				Process string `json:"processId"`
			}
			if err := json.Unmarshal(request.Params, &params); err != nil {
				t.Fatal(err)
			}
			if params.Thread != "main" || params.Process != "42" {
				t.Fatalf("wrong host termination: %+v", params)
			}
			stops++
			if stops == 1 {
				write(map[string]any{"id": request.ID, "error": map[string]any{"code": -32603, "message": "fixture refusal"}})
				continue
			}
			result["terminated"] = true
		}
		write(map[string]any{"id": request.ID, "result": result})
		if request.Method == "thread/start" || request.Method == "thread/backgroundTerminals/terminate" {
			method, status := "item/started", "inProgress"
			if request.Method != "thread/start" {
				method, status = "item/completed", "completed"
			}
			write(map[string]any{"method": method, "params": map[string]any{"threadId": "main", "turnId": "turn", "item": map[string]any{"id": "command", "type": "commandExecution", "command": "sleep 30", "processId": "42", "status": status, "exitCode": 0}}})
		}
	}
}
