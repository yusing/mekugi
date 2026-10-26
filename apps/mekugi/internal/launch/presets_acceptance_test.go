package launch

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/backends/codex/shellhook"
	"github.com/yusing/mekugi/libraries/commandargs"
	conversationbinding "github.com/yusing/mekugi/modules/conversation/codexbinding"
	"github.com/yusing/mekugi/modules/conversation/composer"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
	"github.com/yusing/mekugi/modules/conversation/presets"
)

func TestPresetRecallWaitsForSubmission(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_PRESET_HOST") == "1" {
		servePresetHost(t)
		os.Exit(0)
	}
	t.Setenv(shellhook.Environment, "")
	dir := t.TempDir()
	configPath := filepath.Join(dir, "fixture-config.json")
	t.Setenv("MEKUGI_TEST_PRESET_CONFIG", configPath)
	if err := os.WriteFile(configPath, []byte(`{"1":{"input":"/unlock","label":"Lock"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nMEKUGI_TEST_PRESET_HOST=1; export MEKUGI_TEST_PRESET_HOST\nexec " + commandargs.Quote(os.Args[0]) + " -test.run=^TestPresetRecallWaitsForSubmission$ -- \"$@\"\n"
	executable := filepath.Join(dir, "codex")
	if err := os.WriteFile(executable, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	u, _ := newAppServerTestUI()
	u.attached.cwd = dir
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	launcher := &nativeFixtureLauncher{run: func(runtimeContext context.Context, api *sessionapi.Server) error {
		cmd := exec.CommandContext(runtimeContext, executable, "app-server")
		cmd.Dir = dir
		streams, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
		if err != nil {
			return err
		}
		defer streams.Close()
		conversation := new(conversationmodel.Service)
		observations := conversationbinding.Codex.Observation(conversation)
		wait, err := startNativeSession(u.terminalViews, u.reviewSource, testPresentation(), conversation, u.activity, testHistory(), testPreferences(), testReports(), runtimeContext, cmd, streams, u.controlObservations, u.usage, u.audience, u.review, u.execWindows, u.nativeTrace, u.retained, u.changeSource, u.snapshots, u.recovered, u.publishChanges, u.captureNotice, nil, nil, u.execTrack, false, nil, "", nil, nil, nil, "", nil, &observations, false, nil, api, false, nil)
		if err != nil {
			return err
		}
		return wait()
	}}
	hub := sessionapi.NewHub(ctx, launcher)
	defer hub.Close()
	server := httptest.NewServer(sessionapi.Versioned(hub.Handler()))
	defer server.Close()
	connection, err := sessionapi.StartNative(ctx, server.Client(), server.URL, sessionapi.NativeStart{Workspace: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	var event sessionapi.NativeEvent
	await := func(ready func() bool) {
		t.Helper()
		for !ready() {
			next, e := connection.Next()
			if e != nil {
				t.Fatal(e)
			}
			event = next
		}
	}
	send := func(op sessionapi.NativeOperation) {
		t.Helper()
		if _, e := connection.Send(ctx, event.Target(), op); e != nil {
			t.Fatal(e)
		}
	}
	await(func() bool { return event.Frame.View.Main.ID == "main" })
	draft := composer.Draft{Text: "ordinary draft remains", CursorBack: 3}
	send(sessionapi.NativeOperation{Command: new("/presets"), Editor: &sessionapi.NativeEditorInput{Draft: draft, Target: conversationmodel.EditorTarget{Revision: 7}}})
	await(func() bool { return event.Frame.View.Menu != nil && !event.Frame.View.Menu.Loading })
	choose := func(name string) {
		t.Helper()
		menu := event.Frame.View.Menu
		for _, choice := range menu.Choices {
			if choice.Name == name {
				send(sessionapi.NativeOperation{Menu: &sessionpresentation.MenuAction{Version: event.Frame.View.Version, Menu: menu.ID, Kind: sessionpresentation.MenuAccept, Choice: choice.Identity()}})
				await(func() bool {
					return event.Frame.View.Menu != nil && event.Frame.View.Menu.ID != menu.ID && !event.Frame.View.Menu.Loading
				})
				return
			}
		}
		t.Fatalf("missing preset choice %s", name)
	}
	choose("e")
	choose("1")
	c := nativeTestPromptClient(u)
	c.editor("main").Replace(draft)
	c.accept(event)
	if c.handoff == nil {
		t.Fatal("edit choice did not open the external editor")
	}
	editorPath := filepath.Join(dir, "editor")
	if err := os.WriteFile(editorPath, []byte("#!/bin/sh\nprintf '/lock' > \"$1\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EDITOR", editorPath)
	c.calls = nil
	nativeEditorTestRun(t, c, c.handoff, nil, nil)
	for _, call := range c.calls {
		if call.operation.Menu != nil {
			send(call.operation)
			call.after(nil)
		}
	}
	await(func() bool { return event.Frame.View.Menu != nil && event.Frame.View.Menu.Kind == "preset-label" })
	if event.Frame.View.Menu.Query != "Lock" {
		t.Fatalf("label stage did not offer the current label: %q", event.Frame.View.Menu.Query)
	}
	// Type through the terminal menu, whose keys become label searches and a save.
	for _, key := range []string{"e", "d", "\r"} {
		var label terminalsession.Menu
		label.Update(event.Frame.View)
		action := label.Key(key).Action
		send(sessionapi.NativeOperation{Menu: &action})
		await(func() bool {
			menu := event.Frame.View.Menu
			return menu != nil && (menu.Kind == "preset-confirm" || action.Kind == sessionpresentation.MenuSearch && menu.Query == action.Query)
		})
	}
	if event.Frame.View.Menu.Kind != "preset-confirm" {
		t.Fatalf("label save did not reach confirmation: %+v", event.Frame.View.Menu)
	}
	choose("confirm")
	data, err := os.ReadFile(configPath)
	var slots map[string]presets.Slot
	if err == nil {
		err = json.Unmarshal(data, &slots)
	}
	if err != nil || slots["1"].Input != "/lock" || slots["1"].Label != "Locked" {
		t.Fatalf("external edit was not persisted: %q, %v", slots["1"], err)
	}
	menu := event.Frame.View.Menu
	for _, choice := range menu.Choices {
		if choice.Name == "1" {
			send(sessionapi.NativeOperation{Menu: &sessionpresentation.MenuAction{Version: event.Frame.View.Version, Menu: menu.ID, Kind: sessionpresentation.MenuAccept, Choice: choice.Identity()}})
			break
		}
	}
	var recall *sessionapi.NativeEffect
	await(func() bool {
		for _, e := range event.Frame.Effects {
			if e.Recall != nil {
				recall = new(e)
				return true
			}
		}
		return false
	})
	c.calls = nil
	c.accept(event)
	if c.editor("main").Text != "/lock" {
		t.Fatal("preset did not fill the composer")
	}
	for _, call := range c.calls {
		if call.operation.Composer != nil {
			t.Fatal("recall submitted before explicit send")
		}
	}
	send(sessionapi.NativeOperation{Composer: &sessionapi.NativeComposerInput{Intent: sessionpresentation.ComposerIntent{Thread: "main", Version: event.Frame.View.Version, Target: conversationmodel.EditorTarget{Revision: c.editor("main").Revision()}, Operation: sessionpresentation.SubmitComposer}, Draft: c.editor("main").Draft, Consumed: true}})
	await(func() bool {
		for _, e := range event.Frame.Effects {
			if e.ID > recall.ID && e.Presentation != nil && e.Presentation.InterruptLocked {
				return true
			}
		}
		return false
	})
}

func servePresetHost(t *testing.T) {
	t.Helper()
	path := os.Getenv("MEKUGI_TEST_PRESET_CONFIG")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var slots map[string]presets.Slot
	if err := json.Unmarshal(data, &slots); err != nil {
		t.Fatal(err)
	}
	decoder, encoder := jsontext.NewDecoder(os.Stdin), jsontext.NewEncoder(os.Stdout)
	for {
		var request struct {
			ID     jsontext.Value `json:"id"`
			Method string         `json:"method"`
			Params struct {
				Key   string       `json:"keyPath"`
				Value presets.Slot `json:"value"`
			} `json:"params"`
		}
		if err := json.UnmarshalDecode(decoder, &request); err != nil {
			return
		}
		if len(request.ID) == 0 {
			continue
		}
		result := map[string]any{}
		switch request.Method {
		case "thread/start", "thread/resume":
			cwd, _ := os.Getwd()
			result["thread"] = map[string]any{"id": "main", "cwd": cwd}
		case "config/read":
			config := map[string]any{"tui": map[string]any{"presets": slots}}
			result["config"] = config
			result["layers"] = []any{map[string]any{"name": map[string]any{"type": "user", "file": path}, "version": "v1", "config": config}}
		case "config/value/write":
			slots[strings.TrimPrefix(request.Params.Key, "tui.presets.")] = request.Params.Value
			data, err := json.Marshal(slots)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
		}
		if err := json.MarshalEncode(encoder, map[string]any{"id": request.ID, "result": result}); err != nil {
			t.Fatal(err)
		}
	}
}
