package launch

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/modules/conversation/composer"
	"github.com/yusing/mekugi/modules/conversation/presets"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
)

func TestPresetNativeViewPersistenceRecallAndAdmission(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.startSession("main", t.TempDir())
	n := newNativeViews(t.Context(), sessionapi.NewLive().Native(), u.presentationState, u.terminalViews, u.activity, u.Requests)
	defer n.close()
	if refusal := n.attach(sessionapi.NativeRequest{Attachment: "view", Thread: "main", Workspace: u.attached.cwd, Context: t.Context(), ViewContext: t.Context()}, &u.attached, "main"); refusal != "" {
		t.Fatal(refusal)
	}
	v := n.views["view"]
	publish := func() sessionpresentation.View {
		return publishSessionView(&v.attached, &u.Client, &v.picker, "", preferences.Applied{}, nil, "", "", false, nil, true)
	}
	publish()
	editor := &composer.Editor{Text: "ordinary draft", CursorBack: 3}
	openPresets(t.Context(), &v.attached, &u.Client, &v.picker, editor, preferences.Applied{Model: "example", Effort: "high"}, false)
	answerPresetRead(t, u, map[string]presets.Slot{"1": {Input: "/clear", Label: "Clear"}})
	for deadline := time.Now().Add(2 * time.Second); v.picker.Loading && time.Now().Before(deadline); {
		time.Sleep(time.Millisecond)
		v.picker.pollPresets()
	}
	view := publish()
	if view.Menu == nil || view.Menu.Loading || view.Menu.Problem != "" {
		t.Fatalf("menu: %+v", view.Menu)
	}
	if editor.Text != "ordinary draft" || editor.CursorBack != 3 {
		t.Fatal("opening consumed draft")
	}
	choice := v.picker.Choices[0]
	action := sessionpresentation.MenuAction{Version: view.Version, Menu: view.Menu.ID, Kind: sessionpresentation.MenuAccept, Choice: choice.Identity()}
	applyMenuResult(&v.attached, v.shell, &u.Client, &v.picker, &editor, preferences.Applied{}, &u.serviceTiers, "", "", false, &u.side, u.clock, &u.questions, &u.approvals, false, nil, nil, t.Context(), nil, terminalsession.MenuResult{Action: action, Handled: true})
	if editor.Text != "ordinary draft" || len(v.effects.pending) != 1 || v.effects.pending[0].Recall == nil || *v.effects.pending[0].Recall != "/clear" {
		t.Fatal("recall changed draft or lost input")
	}
	if err := v.attached.presentation.AdmitMenu(action); err == nil {
		t.Fatal("closed menu accepted repeated recall")
	}
	c := nativeTestPromptClient(u)
	c.editor("main").InsertText("keep this draft")
	event := c.event
	event.Frame.Effects = v.effects.publication()
	c.accept(event)
	if c.editor("main").Text != "/clear" || len(c.editor("main").InputHistory) != 1 || c.editor("main").InputHistory[0].Text != "keep this draft" {
		t.Fatal("recall did not fill composer and retain its previous draft")
	}
	for _, call := range c.calls {
		if call.operation.Composer != nil {
			t.Fatal("recall submitted input")
		}
	}
	revision := c.editor("main").Revision()
	c.accept(event)
	if c.editor("main").Revision() != revision {
		t.Fatal("same effect loaded twice")
	}
}

func TestPresetCurrentSettingsSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name, tier, want string
		parsedTier       any
	}{
		{"unset", "", "standard", nil},
		{"default", "default", "standard", nil},
		{"priority", "priority", "fast", "fast"},
		{"flex", "flex", "flex", "flex"},
		{"provider", "provider tier's value", `provider tier'\''s value`, "provider tier's value"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u, _ := newAppServerTestUI()
			u.startSession("main", t.TempDir())
			u.Editor.InsertText("retained draft")
			u.testMainFrame(80, 24, 0)
			openPresets(t.Context(), &u.attached, &u.Client, &u.picker, u.Editor, preferences.Applied{Model: "provider/model", Effort: "high", Tier: tc.tier}, false)
			answerPresetRead(t, u, nil)
			flow := u.picker.presets
			if flow == nil {
				t.Fatal("presets did not open")
			}
			await := func() {
				t.Helper()
				deadline := time.Now().Add(2 * time.Second)
				for flow.Pending && time.Now().Before(deadline) {
					time.Sleep(time.Millisecond)
					flow.Poll()
				}
				if flow.Pending || flow.Problem != "" {
					t.Fatalf("preset storage: pending=%v problem=%s", flow.Pending, flow.Problem)
				}
			}
			choose := func(name, input string) {
				t.Helper()
				for _, choice := range flow.Choices() {
					if choice.Name == name {
						flow.Choose(choice, input)
						return
					}
				}
				t.Fatalf("missing choice %s", name)
			}
			await()
			choose("s", "")
			want := "/model 'provider/model' --reasoning='high' --speed='" + tc.want + "'"
			if flow.Input != want {
				t.Fatalf("settings snapshot %q, want %q", flow.Input, want)
			}
			change, err := sessioncontrol.ParseModelCommand(strings.TrimPrefix(flow.Input, "/model "))
			if err != nil {
				t.Fatalf("saved settings cannot be recalled: %v", err)
			}
			tier, present := change["serviceTier"]
			if change["model"] != "provider/model" || change["effort"] != "high" || !present || tier != tc.parsedTier {
				t.Fatalf("recalled settings = %#v, want model provider/model, high effort and tier %#v", change, tc.parsedTier)
			}
		})
	}
}

func answerPresetRead(t *testing.T, u *appServerUI, slots map[string]presets.Slot) {
	t.Helper()
	select {
	case call := <-u.Requests.Calls():
		u.Host.Input.(*appServerTestInput).requestID = "preset-config"
		u.Client.Dispatch(call)
		request, ok := u.Requests.Lookup("preset-config")
		if !ok || request.Method != "config/read" {
			t.Fatal("preset did not read native configuration")
		}
		value, err := json.Marshal(map[string]any{"config": map[string]any{"tui": map[string]any{"presets": slots}}})
		if err != nil {
			t.Fatal(err)
		}
		u.Requests.CompleteCall(appserver.Message{ID: jsontext.Value("preset-config"), Result: value})
	case <-time.After(2 * time.Second):
		t.Fatal("preset configuration request timed out")
	}
}
