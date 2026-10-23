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
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/libraries/commandargs"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

func TestNativeProgressAdmitsEffortAndReferenceAtOwner(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_PROGRESS_HOST") == "1" {
		serveNativeProgressHost(t)
		os.Exit(0)
	}
	workspace := installAPIRuntimeHost(t)
	bin := t.TempDir()
	script := "#!/bin/sh\nif [ \"$1\" = debug ]; then exit 1; fi\nMEKUGI_TEST_PROGRESS_HOST=1; export MEKUGI_TEST_PROGRESS_HOST\nexec " + commandargs.Quote(os.Args[0]) + " -test.run=^TestNativeProgressAdmitsEffortAndReferenceAtOwner$ -- \"$@\"\n"
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
	await := func(ready func() bool) {
		t.Helper()
		for !ready() {
			event, err = connection.Next()
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	send := func(target sessionapi.NativeTarget, operation sessionapi.NativeOperation) {
		t.Helper()
		result, err := connection.Send(ctx, target, operation)
		if err != nil {
			t.Fatal(err)
		}
		await(func() bool {
			return event.Frame.View.Version.SameAttachment(result.Version) && event.Frame.View.Version.Revision >= result.Version.Revision
		})
	}
	await(func() bool { return event.Frame.View.Main.ID == "main" && event.Frame.View.Caption.Effort == "medium" })
	// Opening the model menu acquires the host's supported effort choices.
	send(event.Target(), sessionapi.NativeOperation{Command: new("/model")})
	await(func() bool { return event.Frame.View.Menu != nil && !event.Frame.View.Menu.Loading })
	menu := event.Frame.View.Menu
	send(event.Target(), sessionapi.NativeOperation{Menu: &sessionpresentation.MenuAction{Version: event.Frame.View.Version, Menu: menu.ID, Kind: sessionpresentation.MenuDismiss}})
	for _, step := range []struct {
		operation sessionpresentation.ComposerOperation
		want      string
	}{{sessionpresentation.IncreaseEffort, "high"}, {sessionpresentation.DecreaseEffort, "medium"}} {
		captured := event
		send(event.Target(), sessionapi.NativeOperation{Command: new("/title progressed " + step.want)})
		if event.Frame.View.Version == captured.Frame.View.Version {
			t.Fatal("fixture did not advance publication")
		}
		// A raw command never occupied a draft, so none may be edited.
		for _, effect := range event.Frame.Effects {
			if effect.Action != nil {
				t.Fatalf("raw command edited a draft: %+v", effect)
			}
		}
		intent := sessionpresentation.ComposerIntent{Version: captured.Frame.View.Version, Thread: captured.Frame.View.Input.ID, Operation: step.operation}
		send(captured.Target(), sessionapi.NativeOperation{Composer: &sessionapi.NativeComposerInput{Intent: intent}})
		await(func() bool { return event.Frame.View.Caption.Effort == step.want })
	}
	captured := event
	reference := sessionpresentation.ReferenceIntent{Version: captured.Frame.View.Version, Thread: captured.Frame.View.Input.ID, Displayed: captured.Frame.View.Displayed, Target: captured.Frame.View.EditorTarget(), Description: "source", Text: "selected answer"}
	send(event.Target(), sessionapi.NativeOperation{Command: new("/title reference progress")})
	send(captured.Target(), sessionapi.NativeOperation{Reference: &reference})
	selections := func() int {
		count := 0
		for _, effect := range event.Frame.Effects {
			if effect.Selection != nil {
				count++
				if effect.Thread != reference.Thread || effect.Selection.Text != reference.Text || effect.Target == nil || *effect.Target != reference.Target {
					t.Fatalf("reference changed its captured selection or editor: %+v", effect)
				}
			}
		}
		return count
	}
	await(func() bool { return selections() == 1 })
	send(event.Target(), sessionapi.NativeOperation{Command: new("/side")})
	await(func() bool {
		return event.Frame.View.SideThread.ID != "" && event.Frame.View.Displayed == event.Frame.View.SideThread
	})
	intent := sessionpresentation.ComposerIntent{Version: captured.Frame.View.Version, Thread: captured.Frame.View.Input.ID, Operation: sessionpresentation.IncreaseEffort}
	send(captured.Target(), sessionapi.NativeOperation{Composer: &sessionapi.NativeComposerInput{Intent: intent}})
	// Both operations retain Main ownership without undoing Side navigation.
	send(captured.Target(), sessionapi.NativeOperation{Reference: &reference})
	await(func() bool { return selections() == 2 })
	if event.Frame.View.Displayed != event.Frame.View.SideThread {
		t.Fatal("captured action changed subsequent display navigation")
	}
	sideShell := sessionpresentation.ComposerIntent{Version: event.Frame.View.Version, Thread: event.Frame.View.SideThread.ID, Operation: sessionpresentation.SubmitComposer}
	if _, err := connection.Send(ctx, event.Target(), sessionapi.NativeOperation{Composer: &sessionapi.NativeComposerInput{Intent: sideShell, Draft: composer.Draft{Text: "!pwd"}}}); err == nil {
		t.Fatal("Side shell command was converted into conversation input")
	}
	send(event.Target(), sessionapi.NativeOperation{Display: &sessionpresentation.DisplayIntent{Version: event.Frame.View.Version, Thread: event.Frame.View.Main}})
	send(event.Target(), sessionapi.NativeOperation{Composer: &sessionapi.NativeComposerInput{Intent: sessionpresentation.ComposerIntent{Version: event.Frame.View.Version, Thread: "main", Operation: sessionpresentation.SubmitComposer}, Draft: composer.Draft{Text: "original input"}}})
	await(func() bool { return event.Frame.View.Interrupt.Turn == "original" && !event.Frame.PendingEffects })
	original := event
	acknowledge := func() {
		t.Helper()
		var effect uint64
		for _, pending := range event.Frame.Effects {
			effect = max(effect, pending.ID)
		}
		send(event.Target(), sessionapi.NativeOperation{Receipt: &sessionapi.NativeReceipt{Effect: effect}})
		await(func() bool { return len(event.Frame.Effects) == 0 })
	}
	acknowledge()
	send(event.Target(), sessionapi.NativeOperation{Composer: &sessionapi.NativeComposerInput{Intent: sessionpresentation.ComposerIntent{Version: event.Frame.View.Version, Thread: "main", Operation: sessionpresentation.QueueComposer}, Draft: composer.Draft{Text: "waiting input"}}})
	for range nativeSemanticEffects - 1 {
		send(event.Target(), sessionapi.NativeOperation{Reference: &sessionpresentation.ReferenceIntent{Version: event.Frame.View.Version, Thread: "main", Displayed: event.Frame.View.Main, Text: "retained selection"}})
	}
	// The waiting input owns the last consequence slot. Restoring it must not
	// need another slot or wait for this paused consumer's earlier receipts.
	send(original.Target(), sessionapi.NativeOperation{Composer: &sessionapi.NativeComposerInput{Intent: sessionpresentation.ComposerIntent{Version: original.Frame.View.Version, Thread: "main", Operation: sessionpresentation.RestoreWaiting, Work: new(original.Frame.View.Interrupt)}}})
	await(func() bool {
		for _, effect := range event.Frame.Effects {
			if effect.Restore != nil && effect.Restore.Text == "waiting input" {
				return true
			}
		}
		return false
	})
	if len(event.Frame.Effects) != nativeSemanticEffects || event.Frame.View.Interrupt.Turn != "original" {
		t.Fatal("full-capacity restoration lost a consequence or changed running work")
	}
	acknowledge()
	send(event.Target(), sessionapi.NativeOperation{Command: new("/title finish-original")})
	await(func() bool { return event.Frame.View.Interrupt.Turn == "successor" })
	for _, mode := range []sessioncontrol.InterruptMode{sessioncontrol.RestoreWaiting, sessioncontrol.ExpediteSteers} {
		send(original.Target(), sessionapi.NativeOperation{Interrupt: &sessionpresentation.InterruptIntent{Version: original.Frame.View.Version, Target: original.Frame.View.Interrupt, Mode: mode}})
		await(func() bool { return event.Frame.View.Status.Feedback.Text == "Original work already ended." })
		if event.Frame.View.Interrupt.Turn != "successor" || event.Frame.View.Status.Feedback.Alert {
			t.Fatal("completed original interruption affected its successor or became an error")
		}
	}
	send(original.Target(), sessionapi.NativeOperation{Composer: &sessionapi.NativeComposerInput{Intent: sessionpresentation.ComposerIntent{Version: original.Frame.View.Version, Thread: "main", Operation: sessionpresentation.RestoreWaiting, Work: new(original.Frame.View.Interrupt)}}})
	if event.Frame.View.Interrupt.Turn != "successor" {
		t.Fatal("completed original restoration affected its successor")
	}
}

func serveNativeProgressHost(t *testing.T) {
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
			workspace, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			result["thread"] = map[string]any{"id": "main", "cwd": workspace}
			result["model"], result["reasoningEffort"] = "fixture-model", "medium"
		case "model/list":
			result["data"] = []any{map[string]any{"model": "fixture-model", "defaultReasoningEffort": "medium", "supportedReasoningEfforts": []any{map[string]any{"reasoningEffort": "low"}, map[string]any{"reasoningEffort": "medium"}, map[string]any{"reasoningEffort": "high"}}}}
		case "thread/fork":
			result["thread"] = map[string]any{"id": "side"}
		case "turn/start":
			result["turn"] = map[string]any{"id": "original"}
		case "turn/interrupt":
			t.Fatal("completed original action reached host interruption")
		}
		write(map[string]any{"id": request.ID, "result": result})
		if request.Method == "turn/start" {
			write(map[string]any{"method": "turn/started", "params": map[string]any{"threadId": "main", "turn": map[string]any{"id": "original"}}})
			write(map[string]any{"method": "item/completed", "params": map[string]any{"threadId": "main", "turnId": "original", "item": map[string]any{"id": "user", "type": "userMessage", "content": []any{map[string]any{"type": "text", "text": "original input"}}}}})
		}
		if request.Method == "thread/name/set" {
			var params struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(request.Params, &params); err != nil {
				t.Fatal(err)
			}
			if params.Name == "finish-original" {
				write(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "main", "turn": map[string]any{"id": "original", "status": "completed"}}})
				write(map[string]any{"method": "turn/started", "params": map[string]any{"threadId": "main", "turn": map[string]any{"id": "successor"}}})
			}
		}
		if request.Method == "thread/settings/update" {
			var params struct {
				Thread string `json:"threadId"`
				Effort string `json:"effort"`
			}
			if err := json.Unmarshal(request.Params, &params); err != nil {
				t.Fatal(err)
			}
			write(map[string]any{"method": "thread/settings/updated", "params": map[string]any{"threadId": params.Thread, "threadSettings": map[string]any{"model": "fixture-model", "effort": params.Effort}}})
		}
	}
}
