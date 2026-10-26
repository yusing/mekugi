package launch

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
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
	stale := process
	stale.Item = "replaced"
	if _, err := connection.Send(ctx, event.Target(), sessionapi.NativeOperation{StopProcess: &stale}); err == nil {
		t.Fatal("stale process reached host dispatch")
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
