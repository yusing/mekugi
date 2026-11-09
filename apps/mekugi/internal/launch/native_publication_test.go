package launch

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/backends/codex/messagehistory"
	"github.com/yusing/mekugi/modules/conversation/completion"
	"github.com/yusing/mekugi/modules/conversation/composer"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
)

func TestNativeMainHistoryRolloverKeepsAcknowledgedImages(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.startSession("main", "/workspace")
	// Keep persistent provenance on every occurrence, as Main does when shared
	// input history is enabled, without starting a storage worker.
	u.attached.inputHistory = &appServerInputHistory{thread: "main"}
	c := nativeTestPromptClient(u)
	for i := range 101 {
		var input composer.Editor
		input.InsertImage(fmt.Sprintf("/image-%d.png", i))
		draft := input.Draft
		id := fmt.Sprintf("input-%d", i)
		u.attached.presentation.StageInput(&u.Live, u.attached.conversation, id, conversationmodel.Submission{Draft: draft, Parts: []conversationmodel.Input{{Draft: draft}}}, false)
		effects := u.attached.presentation.AcknowledgeInput(&u.Live, u.attached.conversation, "turn/start", "", false)
		applyInputEffects(&u.attached, nil, nil, &u.picker, "", u.clock, effects)
		facts := nativePublicationFacts(&u.attached, nil, binding.TokenUsage{}, "", false, nil, "main", nil)
		wire, err := json.Marshal(facts)
		if err != nil {
			t.Fatal(err)
		}
		var received sessionapi.NativeSessionFacts
		if err := json.Unmarshal(wire, &received); err != nil {
			t.Fatal(err)
		}
		c.accept(sessionapi.NativeEvent{Attachment: "view", Frame: sessionapi.NativeFrame{View: u.attached.presentation.View(), Facts: received}})
	}
	editor := c.editor("main")
	if len(editor.InputHistory) != 100 {
		t.Fatalf("history length = %d", len(editor.InputHistory))
	}
	for i := 100; i > 0; i-- {
		if !editor.Recall(true) || len(editor.Images) != 1 || editor.Images[0].Path != fmt.Sprintf("/image-%d.png", i) {
			t.Fatalf("recall %d: %+v", i, editor.Draft)
		}
	}
}

func TestNativeMainHistoryRolloverRestoresBrowsingDraft(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.startSession("main", "/workspace")
	c := nativeTestPromptClient(u)
	publish := func(i int) {
		rememberInput(u.attached.conversation, conversationmodel.Input{Text: fmt.Sprintf("entry-%d", i)})
		c.event.Frame.Facts = nativePublicationFacts(&u.attached, nil, binding.TokenUsage{}, "", false, nil, "main", nil)
		c.acceptFacts("main")
	}
	for i := range 100 {
		publish(i)
	}
	editor := c.editor("main")
	editor.Insert("unsent draft")
	editor.CursorBack = 3
	for range 100 {
		if !editor.Recall(true) {
			t.Fatal("could not browse to oldest entry")
		}
	}
	publish(100)
	if editor.Text != "unsent draft" || editor.CursorBack != 3 || editor.HistoryBack != 0 {
		t.Fatalf("rollover lost native draft: %+v", editor)
	}
	if !editor.Recall(true) || editor.Text != "entry-100" || !editor.Recall(false) || editor.Text != "unsent draft" || editor.CursorBack != 3 {
		t.Fatalf("native recall overwrote restored draft: %+v", editor)
	}
}

func TestNativeHistoryEnrichesPersistedRepeatedOccurrences(t *testing.T) {
	for _, sharedFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("sharedFirst=%t", sharedFirst), func(t *testing.T) {
			u, _ := newAppServerTestUI()
			u.startSession("main", "/workspace")
			c := nativeTestPromptClient(u)
			h := &appServerInputHistory{}
			loaded := historyLoad{entries: []messagehistory.Entry{
				{SessionID: "other", Text: "[Image 1]"},
				{SessionID: "main", Text: "[Image 1]"},
				{SessionID: "main", Text: "[Image 1]"},
			}}
			publish := func() {
				c.event.Frame.Facts = nativePublicationFacts(&u.attached, nil, binding.TokenUsage{}, "", false, nil, "main", nil)
				c.acceptFacts("main")
			}
			if sharedFirst {
				h.loaded(u.attached.conversation, loaded)
				publish()
			}
			for _, path := range []string{"/first.png", "/second.png"} {
				var input composer.Editor
				input.InsertImage(path)
				u.attached.conversation.Editor.RestoreHistory("main", input.Draft)
			}
			publish()
			if !sharedFirst {
				h.loaded(u.attached.conversation, loaded)
			}
			publish()
			publish()
			editor := c.editor("main")
			if len(editor.InputHistory) != 3 || editor.InputHistory[0].Source != "other" {
				t.Fatalf("persisted occurrences lost or duplicated: %+v", editor.InputHistory)
			}
			for _, path := range []string{"/second.png", "/first.png"} {
				if !editor.Recall(true) || len(editor.Images) != 1 || editor.Images[0].Path != path {
					t.Fatalf("recall %s: %+v", path, editor.Draft)
				}
				entry := editor.InputHistory[len(editor.InputHistory)-editor.HistoryBack]
				if entry.Source != "main" || entry.Occurrence == "" {
					t.Fatalf("live identity lost: %+v", entry)
				}
			}
		})
	}
}

func TestNativeFactsRestoreHistoryPreferencesAndUsage(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	u, _ := newAppServerTestUI()
	u.attached.cwd = t.TempDir()
	panes := testPanePreferences()
	if err := panes.openTerminal(&u.shell.Shell, u.shell.Main, u.attached.cwd, "main", false); err != nil {
		t.Fatal(err)
	}
	panes.attachment.StagePresentation(jsontext.Value(`{"focus":0,"split":62,"diffOpen":true,"interruptLocked":true}`))
	u.attached.conversation.Editor.RestoreHistory("main", composer.Draft{Text: "same"})
	u.attached.conversation.Editor.RestoreHistory("main", composer.Draft{Text: "same"})
	u.testMainFrame(80, 24, 0)
	facts := nativePublicationFacts(&u.attached, panes, binding.TokenUsage{TotalTokens: 14, InputTokens: 10, CachedInputTokens: 4, OutputTokens: 4}, "/debug", false, nil, "main", nil)
	wire, err := json.Marshal(&facts)
	if err != nil {
		t.Fatal(err)
	}
	var received sessionapi.NativeSessionFacts
	if err := json.Unmarshal(wire, &received); err != nil {
		t.Fatal(err)
	}
	terminal, _ := u.terminalViews.NewAttachment(u.shell.Transcript.Painter.Theme)
	terminal.Width, terminal.Height = 80, 24
	c := &nativeClient{renderer: nativeClientRenderer{terminal: terminal, views: u.terminalViews}}
	c.editor("main").Remember(composer.Draft{Text: "local rejected shell"})
	event := sessionapi.NativeEvent{Attachment: "view", Frame: sessionapi.NativeFrame{View: u.attached.presentation.View(), Facts: received}}
	c.accept(event)
	c.accept(event)
	if len(c.editor("main").InputHistory) != 3 {
		t.Fatalf("history duplicated or lost: %+v", c.editor("main").InputHistory)
	}
	c.editor("main").Recall(true)
	if c.editor("main").Text != "local rejected shell" {
		t.Fatal("local recall entry displaced")
	}
	if !terminal.Main.InterruptLocked || terminal.Split != 62 {
		t.Fatalf("preferences not restored: %+v", terminal.Shell.Preferences(terminal.Main))
	}
	var exit bytes.Buffer
	if err := writeExitSummary(binding.TokenUsage(c.event.Frame.Facts.Usage), false, "main", []string{"mekugi", "codex"}, "", &exit, false); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(exit.Bytes(), []byte("total=10 input=6 (+ 4 cached) output=4")) {
		t.Fatalf("usage: %s", exit.String())
	}
}

func TestNativeCompletionContinuesNewestTargetWithoutEditorCache(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.attached.cwd, u.attached.view, u.picker.view = "/work", "view", "view"
	editor := &composer.Editor{Text: "@first"}
	u.picker.refreshPicker(&u.attached, u.clock, u.Requests, editor, context.Background(), nil)
	editor.Replace(composer.Draft{Text: "@second"})
	u.picker.refreshPicker(&u.attached, u.clock, u.Requests, editor, context.Background(), nil)
	u.picker.pickerMessage(&u.attached, u.clock, u.Requests, new(composer.Editor), context.Background(), nil, "fuzzyFileSearch", appserver.Message{Result: jsontext.Value(`{"files":[]}`)})
	requests := pickerRequests(t, wire)
	if len(requests) != 2 || requests[1].Params.Query != "second" {
		t.Fatalf("latest target lost: %+v", requests)
	}
}

func TestNativePreferenceWriterRetainsOutgoingOwnersInOrder(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	u, _ := newAppServerTestUI()
	p := testPanePreferences()
	workspace := t.TempDir()
	if err := p.openTerminal(&u.shell.Shell, u.shell.Main, workspace, "first", false); err != nil {
		t.Fatal(err)
	}
	p.attachment.StagePresentation(jsontext.Value(`{"focus":0,"split":61,"diffOpen":true}`))
	if err := p.savePrepared(time.Now(), true); err != nil {
		t.Fatal(err)
	}
	if err := p.openTerminal(&u.shell.Shell, u.shell.Main, workspace, "second", false); err != nil {
		t.Fatal(err)
	}
	p.attachment.StagePresentation(jsontext.Value(`{"focus":0,"split":42,"diffOpen":true}`))
	if err := p.openTerminal(&u.shell.Shell, u.shell.Main, workspace, "first", true); err != nil {
		t.Fatal(err)
	}
	if got := u.shell.Shell.Preferences(u.shell.Main).Split; got != 61 {
		t.Fatalf("revisit lost pending state: %d", got)
	}
	p.attachment.StagePresentation(jsontext.Value(`{"focus":0,"split":73,"diffOpen":true}`))
	if err := p.writer.close(p.attachment, time.Now()); err != nil {
		t.Fatal(err)
	}
	for thread, want := range map[string]int{"first": 73, "second": 42} {
		saved, err := p.service.Read(preferences.Scope{Workspace: workspace, Thread: thread})
		if err != nil {
			t.Fatal(err)
		}
		state, err := terminalsession.ReadPreferences(saved.Presentation)
		if err != nil || state.Split != want {
			t.Fatalf("%s: %+v %v", thread, state, err)
		}
	}
}

func TestNativeControlCCapturesObservedWork(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.startSession("main", "/workspace")
	appServerTestTurn(t, u, "turn")
	c := nativeTestPromptClient(u)
	c.dispatch(terminalsession.Input{Kind: terminalsession.KeyInput, Text: "\x03"})
	if len(c.calls) != 1 || c.calls[0].operation.Interrupt == nil || c.calls[0].operation.Composer != nil {
		t.Fatalf("Ctrl-C did not capture its interrupt target: %+v", c.calls)
	}
	intent := *c.calls[0].operation.Interrupt
	if intent.Target != u.Live.InterruptTarget() || intent.Mode != sessioncontrol.RestoreWaiting {
		t.Fatalf("wrong interruption: %+v", intent)
	}
	u.attached.presentation.Status.Set("More progress", false)
	u.testMainFrame(80, 24, 0)
	if active, err := u.attached.presentation.ResolveInterrupt(intent, &u.Live, &u.attached.session); !active || err != nil {
		t.Fatalf("progress rejected Ctrl-C: %v", err)
	}
	u.Turn = "replacement"
	if active, err := u.attached.presentation.ResolveInterrupt(intent, &u.Live, &u.attached.session); active || err == nil {
		t.Fatal("old Ctrl-C admitted against replacement work")
	}
}

func TestNativeEditorEffectKeepsOriginatingDraft(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprintf("changed=%t", changed), func(t *testing.T) {
			u, _ := newAppServerTestUI()
			u.startSession("main", "/workspace")
			c := nativeTestPromptClient(u)
			main := c.editor("main")
			main.InsertText("/oneoff initial task")
			_, target := c.selectedEditor()
			if changed {
				main.InsertText(" newer")
			}
			side := c.editor("side:side")
			side.InsertText("private side draft")
			event := c.event
			event.Frame.View.Input = sessionpresentation.Thread{ID: "side"}
			event.Frame.View.Displayed = event.Frame.View.Input
			event.Frame.Effects = []sessionapi.NativeEffect{{ID: 1, Thread: "main", Target: &target, Action: &composer.Action{Kind: composer.DeleteAction, End: len("/oneoff initial task")}}}
			c.accept(event)
			want := ""
			if changed {
				want = "/oneoff initial task newer"
			}
			if main.Text != want || side.Text != "private side draft" || c.effect != 1 {
				t.Fatalf("effect lost its owner or revision guard: main=%q side=%q receipt=%d", main.Text, side.Text, c.effect)
			}
			if (c.feedback.Text != "") != changed {
				t.Fatalf("stale target feedback: %q", c.feedback.Text)
			}
		})
	}
}

func TestNativeLocalShellModeAndStaleCompletion(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.attached.cwd, u.attached.view = "/work", "view"
	u.testMainFrame(80, 24, 0)
	terminal, _ := u.terminalViews.NewAttachment(u.shell.Transcript.Painter.Theme)
	c := &nativeClient{renderer: nativeClientRenderer{terminal: terminal, views: u.terminalViews}}
	c.editor("main").InsertText("!echo $term")
	c.accept(sessionapi.NativeEvent{Attachment: "view", Frame: sessionapi.NativeFrame{View: u.attached.presentation.View()}})
	if !c.renderer.frame.View.Status.ShellMode {
		t.Fatal("local shell draft lost its semantic presentation")
	}
	u.picker.refreshPicker(&u.attached, u.clock, u.Requests, c.editor("main"), t.Context(), nil)
	if u.picker.Open || wire.Len() != 0 {
		t.Fatal("shell text acquired feature completion")
	}
	c.editor("main").Replace(composer.Draft{Text: "@old"})
	_, target := c.selectedEditor()
	c.event.Frame.CompletionTarget = target
	c.event.Frame.View.Menu = &sessionpresentation.MenuView{ID: 1, Kind: sessionpresentation.MenuCompletion, View: completion.View{}}
	c.refresh(time.Now())
	if c.renderer.frame.View.Menu == nil {
		t.Fatal("matching completion was hidden")
	}
	c.editor("main").InsertText("new")
	c.refresh(time.Now())
	if c.renderer.frame.View.Menu != nil {
		t.Fatal("stale completion remained actionable")
	}
}

func TestNativePredictionDismissalAndSkillBindingAreClientLocal(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.testMainFrame(80, 24, 0)
	terminal, _ := u.terminalViews.NewAttachment(u.shell.Transcript.Painter.Theme)
	c := &nativeClient{renderer: nativeClientRenderer{terminal: terminal, views: u.terminalViews}}
	frame := sessionapi.NativeFrame{View: u.attached.presentation.View(), Facts: sessionapi.NativeSessionFacts{PredictionThread: "main", PredictionTurn: "turn"}}
	frame.View.Draft.Prediction = "Show tests"
	c.accept(sessionapi.NativeEvent{Attachment: "view", Frame: frame})
	if c.renderer.frame.View.Draft.Prediction != "Show tests" {
		t.Fatal("prediction not offered")
	}
	c.editor("main").InsertText("mine")
	c.refresh(time.Now())
	c.editor("main").Take()
	c.refresh(time.Now())
	if c.renderer.frame.View.Draft.Prediction != "" {
		t.Fatal("typing and clearing revived prediction")
	}
	frame.View.Draft.Prediction = ""
	c.accept(sessionapi.NativeEvent{Attachment: "view", Frame: frame})
	frame.View.Draft.Prediction = "Show tests"
	c.accept(sessionapi.NativeEvent{Attachment: "view", Frame: frame})
	if c.renderer.frame.View.Draft.Prediction != "" {
		t.Fatal("hidden publication revived dismissed prediction")
	}
	frame.Facts.PredictionTurn = "next"
	c.accept(sessionapi.NativeEvent{Attachment: "view", Frame: frame})
	if c.renderer.frame.View.Draft.Prediction != "Show tests" {
		t.Fatal("new completed turn prediction suppressed")
	}
	c.editor("main").Replace(composer.Draft{Text: "$skill "})
	_, target := c.selectedEditor()
	skills := []composer.Token{{Name: "skill", Path: "/skills/SKILL.md", Start: 0, End: 6}}
	frame.Effects = []sessionapi.NativeEffect{{ID: 1, Target: &target, Skills: &skills}}
	c.accept(sessionapi.NativeEvent{Attachment: "view", Frame: frame})
	if len(c.editor("main").Skills) != 1 || c.editor("main").Text != "$skill " {
		t.Fatal("guarded auto-binding changed draft")
	}
	c.editor("main").Replace(composer.Draft{Text: "later"})
	frame.Effects[0].ID = 2
	c.accept(sessionapi.NativeEvent{Attachment: "view", Frame: frame})
	if len(c.editor("main").Skills) != 0 || c.editor("main").Text != "later" {
		t.Fatal("late binding replaced newer editor")
	}
}

func TestNativeSideCompletionKeepsOriginatingDraft(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.startSession("main", "/workspace")
	n := newNativeViews(t.Context(), sessionapi.NewLive().Native(), u.presentationState, u.activity, u.Requests)
	defer n.close()
	if refusal := n.attach(sessionapi.NativeRequest{Attachment: "view", Thread: "main", Workspace: "/workspace", Context: t.Context(), ViewContext: t.Context()}, &u.attached, "main"); refusal != "" {
		t.Fatal(refusal)
	}
	v := n.views["view"]
	sideThread := sessionpresentation.Thread{ID: "side"}
	v.attached.presentation.SetSide(sideThread)
	if err := v.attached.presentation.Display(sessionpresentation.DisplayIntent{Version: v.attached.presentation.View().Version, Thread: sideThread}); err != nil {
		t.Fatal(err)
	}
	v.picker.Skills = []completion.Choice{{Name: "skill", Path: "/skills/SKILL.md", Enabled: true}}
	v.picker.SkillsLoaded, v.picker.SkillsCwd = true, "/workspace"
	v.picker.RememberSkills()
	c := nativeTestPromptClient(u)
	c.accept(sessionapi.NativeEvent{Attachment: "view", Frame: sessionapi.NativeFrame{View: v.attached.presentation.View()}})
	main, side := c.editor("main"), c.editor("side:side")
	main.InsertText("parked Main draft")
	for _, changed := range []bool{false, true} {
		side.Replace(composer.Draft{Text: "$skill "})
		_, target := c.selectedEditor()
		v.complete(sessionapi.NativeEditorInput{Target: target, Draft: side.Draft.Clone()}, u.clock, u.Requests, nil)
		if len(v.effects.pending) != 1 || v.effects.pending[0].Thread != "side" || v.effects.pending[0].Skills == nil {
			t.Fatalf("completion lost its Side owner: %+v", v.effects.pending)
		}
		if changed {
			side.Replace(composer.Draft{Text: "new Side draft"})
		}
		c.accept(sessionapi.NativeEvent{Attachment: "view", Frame: sessionapi.NativeFrame{View: v.attached.presentation.View(), Effects: v.effects.pending}})
		if main.Text != "parked Main draft" || len(main.Skills) != 0 || !changed && (side.Text != "$skill " || len(side.Skills) != 1) || changed && (side.Text != "new Side draft" || len(side.Skills) != 0) {
			t.Fatalf("completion crossed drafts or revisions: changed=%t main=%+v side=%+v", changed, main.Draft, side.Draft)
		}
		v.effects.acknowledge(c.effect, false)
	}
}

func TestNativeSkillChangeInvalidatesEachViewCatalog(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.startSession("main", "/workspace")
	n := newNativeViews(t.Context(), sessionapi.NewLive().Native(), u.presentationState, u.activity, u.Requests)
	defer n.close()
	for _, id := range []string{"one", "two"} {
		request := sessionapi.NativeRequest{Attachment: id, Thread: "main", Workspace: "/workspace", Context: t.Context(), ViewContext: t.Context()}
		if reason := n.attach(request, &u.attached, u.Thread); reason != "" {
			t.Fatal(reason)
		}
		p := &n.views[id].picker
		p.SkillsLoaded = true
		p.SkillsProblem = "old"
		p.Resolved = completion.Target{Kind: '$', Query: "skill"}
	}
	n.invalidateSkills(u.attached.executionPresentation)
	for _, v := range n.views {
		if v.picker.SkillsLoaded || v.picker.SkillsProblem != "" || v.picker.Resolved.Kind != 0 || v.picker.skillsRevision != 1 {
			t.Fatal("view kept stale skill catalog")
		}
	}
}
