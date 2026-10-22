package launch

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"
	"github.com/yusing/mekugi/backends/codex/shellhook"
	"github.com/yusing/mekugi/libraries/commandargs"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
	conversationbinding "github.com/yusing/mekugi/modules/conversation/codexbinding"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
)

// Exercise menu admission on the running socket owner, rather than the
// keyboard adapter, which can independently dispatch a staged reversal.
func TestNativeRewindRevertSocket(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_REWIND_HOST") == "1" {
		serveNativeRewindHost(t)
		os.Exit(0)
	}
	for _, outcome := range []string{"success", "conflict"} {
		t.Run(outcome, func(t *testing.T) {
			t.Setenv(shellhook.Environment, "")
			u, _ := rewindEditsFixture(t)
			expected := u.picker.rewind.command
			projection := u.review.Projection
			u.review = sessionreview.New(t.Context(), previewmodel.New(changecapture.ReadFile), nil)
			u.review.IncludeThread(u.attached.cwd, "main", true)
			u.review.IncludeThread(u.attached.cwd, "child", false)
			projection.Scope = u.review.Scope()
			u.review.Projection = projection
			sources := map[string]string{}
			for _, source := range u.picker.rewind.sources {
				sources[source.ID] = source.Path
			}
			encoded, err := json.Marshal(sources)
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("MEKUGI_TEST_REWIND_SOURCES", string(encoded))
			directory := t.TempDir()
			if err := os.Chmod(directory, 0700); err != nil {
				t.Fatal(err)
			}
			logPath, gate := filepath.Join(directory, "host.log"), filepath.Join(directory, "complete")
			t.Setenv("MEKUGI_TEST_REWIND_LOG", logPath)
			t.Setenv("MEKUGI_TEST_REWIND_GATE", gate)
			t.Setenv("MEKUGI_TEST_REWIND_OUTCOME", outcome)
			script := "#!/bin/sh\nMEKUGI_TEST_REWIND_HOST=1; export MEKUGI_TEST_REWIND_HOST\nexec " + commandargs.Quote(os.Args[0]) + " -test.run=^TestNativeRewindRevertSocket$ -- \"$@\"\n"
			executable := filepath.Join(directory, "codex")
			writeTestFile(t, executable, script)
			if err := os.Chmod(executable, 0700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			launcher := &nativeFixtureLauncher{run: func(runtimeContext context.Context, api *sessionapi.Server) error {
				cmd := exec.CommandContext(runtimeContext, executable, "app-server")
				cmd.Dir = u.attached.cwd
				streams, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
				if err != nil {
					return err
				}
				defer streams.Close()
				conversation := new(conversationmodel.Service)
				observations := conversationbinding.Codex.Observation(conversation)
				wait, err := startNativeSession(u.terminalViews, u.reviewSource, testPresentation(), conversation, u.activity, testHistory(), testPreferences(), testReports(), runtimeContext, cmd, streams, u.controlObservations, u.usage, u.audience, u.review, u.execWindows, u.nativeTrace, u.review.Projection.Retained, u.review.Projection.Changes, u.snapshots, u.recovered, u.publishChanges, u.captureNotice, nil, nil, u.execTrack, false, nil, "", nil, nil, nil, "", nil, &observations, false, nil, api, false)
				if err != nil {
					return err
				}
				return wait()
			}}
			hub := sessionapi.NewHub(ctx, launcher)
			defer hub.Close()
			listener, err := sessionapi.ListenControl(filepath.Join(directory, "s"))
			if err != nil {
				t.Fatal(err)
			}
			server := &http.Server{Handler: sessionapi.Versioned(hub.Handler())}
			served := make(chan error, 1)
			go func() { served <- server.Serve(listener) }()
			defer func() { server.Close(); <-served }()
			transport := sessionapi.ControlTransport(filepath.Join(directory, "s"))
			defer transport.CloseIdleConnections()
			connection, err := sessionapi.StartNative(ctx, &http.Client{Transport: transport}, "http://mekugi", sessionapi.NativeStart{Workspace: u.attached.cwd})
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Close()
			var event sessionapi.NativeEvent
			await := func(ready func() bool) {
				t.Helper()
				for !ready() {
					next, nextErr := connection.Next()
					if nextErr != nil {
						t.Fatalf("%v; menu=%+v", nextErr, event.Frame.View.Menu)
					}
					event = next
				}
			}
			send := func(operation sessionapi.NativeOperation) {
				t.Helper()
				if _, err := connection.Send(ctx, event.Target(), operation); err != nil {
					t.Fatal(err)
				}
			}
			choose := func(name string) {
				t.Helper()
				await(func() bool {
					menu := event.Frame.View.Menu
					if menu == nil || menu.Loading {
						return false
					}
					for _, choice := range menu.Choices {
						if choice.Name == name {
							return true
						}
					}
					return false
				})
				menu := event.Frame.View.Menu
				for _, choice := range menu.Choices {
					if choice.Name == name {
						send(sessionapi.NativeOperation{Menu: &sessionpresentation.MenuAction{Version: event.Frame.View.Version, Menu: menu.ID, Kind: sessionpresentation.MenuAccept, Choice: choice.Identity()}})
						return
					}
				}
			}
			await(func() bool {
				return event.Frame.View.Main.ID == "main" && event.Frame.Main != nil && len(event.Frame.Main.Entries) > 0 && event.Frame.Review != nil && len(event.Frame.Review.Files) > 0 && event.Frame.Review.Coverage != "CONNECTING"
			})
			send(sessionapi.NativeOperation{Command: new("/rewind")})
			choose("second")
			choose("Revert 2 captured changes")
			// Admission completes before the host reads its pipe. Bound that drain,
			// then assert the command itself so missing dispatch is explicit.
			for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
				body, _ := os.ReadFile(logPath)
				if strings.Contains(string(body), "thread/shellCommand ") {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			body, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(body), "thread/fork ") {
				t.Fatal("fork preceded shell completion")
			}
			if strings.Count(string(body), "thread/shellCommand ") != 1 {
				t.Fatalf("shell dispatch count: %s", body)
			}
			var shell struct {
				Thread  string `json:"threadId"`
				Command string `json:"command"`
			}
			for line := range strings.SplitSeq(string(body), "\n") {
				if params, ok := strings.CutPrefix(line, "thread/shellCommand "); ok {
					if err := json.Unmarshal([]byte(params), &shell); err != nil {
						t.Fatal(err)
					}
				}
			}
			if shell.Thread != "main" || shell.Command != expected {
				t.Fatalf("reversal = %+v, want main %q", shell, expected)
			}
			writeTestFile(t, gate, "complete")
			if outcome == "success" {
				serverCLIAwait(t, func() bool { body, _ := os.ReadFile(logPath); return strings.Contains(string(body), "thread/fork ") })
				body, _ := os.ReadFile(logPath)
				if strings.Count(string(body), "thread/fork ") != 1 || !strings.Contains(string(body), `"beforeTurnId":"second"`) {
					t.Fatalf("fork boundary: %s", body)
				}
			} else {
				await(func() bool { return strings.Contains(event.Frame.View.Status.Feedback.Text, "Revert incomplete") })
				if event.Frame.View.Main.ID != "main" {
					t.Fatal("conflict replaced original session")
				}
				send(sessionapi.NativeOperation{Command: new("/rewind")})
				await(func() bool {
					return event.Frame.View.Menu != nil && event.Frame.View.Menu.Kind == sessionpresentation.MenuRewind && !event.Frame.View.Menu.Loading
				})
				body, _ := os.ReadFile(logPath)
				if strings.Contains(string(body), "thread/fork ") {
					t.Fatal("conflict forked the session")
				}
			}
		})
	}
}

func serveNativeRewindHost(t *testing.T) {
	t.Helper()
	log, err := os.OpenFile(os.Getenv("MEKUGI_TEST_REWIND_LOG"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	sources := map[string]string{}
	if err := json.Unmarshal([]byte(os.Getenv("MEKUGI_TEST_REWIND_SOURCES")), &sources); err != nil {
		t.Fatal(err)
	}
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
		fmt.Fprintf(log, "%s %s\n", request.Method, request.Params)
		result := map[string]any{}
		switch request.Method {
		case "thread/start", "thread/resume", "thread/fork":
			cwd, _ := os.Getwd()
			id := "main"
			if request.Method == "thread/fork" {
				id = "rewound"
			}
			result["thread"] = map[string]any{"id": id, "cwd": cwd}
		case "thread/read":
			var params struct {
				Thread string `json:"threadId"`
			}
			if err := json.Unmarshal(request.Params, &params); err != nil {
				t.Fatal(err)
			}
			result["thread"] = map[string]any{"id": params.Thread, "path": sources[params.Thread], "turns": []any{map[string]any{"id": "second", "status": "completed", "items": []any{map[string]any{"type": "userMessage", "content": []any{map[string]any{"type": "text", "text": "Change the error format"}}}}}}}
		case "turn/start":
			result["turn"] = map[string]any{"id": "next"}
		}
		write(map[string]any{"id": request.ID, "result": result})
		if request.Method == "thread/start" || request.Method == "thread/resume" {
			write(map[string]any{"method": "item/completed", "params": map[string]any{"threadId": "main", "turnId": "first", "item": map[string]any{"type": "userMessage", "id": "user", "content": []any{map[string]any{"type": "text", "text": "Implement the parser"}}}}})
		}
		if request.Method == "thread/shellCommand" {
			write(map[string]any{"method": "turn/started", "params": map[string]any{"threadId": "main", "turn": map[string]any{"id": "shell"}}})
			write(map[string]any{"method": "item/started", "params": map[string]any{"threadId": "main", "turnId": "shell", "item": map[string]any{"id": "revert", "type": "commandExecution", "source": "userShell", "status": "inProgress"}}})
			for {
				if _, err := os.Stat(os.Getenv("MEKUGI_TEST_REWIND_GATE")); err == nil {
					break
				}
				time.Sleep(time.Millisecond)
			}
			exit := 0
			if os.Getenv("MEKUGI_TEST_REWIND_OUTCOME") == "conflict" {
				exit = 1
			}
			write(map[string]any{"method": "item/completed", "params": map[string]any{"threadId": "main", "turnId": "shell", "item": map[string]any{"id": "revert", "type": "commandExecution", "source": "userShell", "status": "completed", "exitCode": exit}}})
			write(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "main", "turn": map[string]any{"id": "shell", "status": "completed"}}})
		}
	}
}
