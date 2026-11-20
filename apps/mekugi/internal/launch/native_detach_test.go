package launch

import (
	"slices"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/diffrender"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func nativeTestExitRequested(c *nativeClient) bool {
	return len(c.calls) == 1 && c.calls[0].operation.Command != nil && *c.calls[0].operation.Command == "/quit"
}

func TestNativeTerminalExitScopeUsesPendingWorkAndExcludesSiblings(t *testing.T) {
	mainUI, _ := newAppServerTestUI()
	main := &mainUI.nativeSession
	main.Client.Live.Thread = "main"
	childUI, _ := newAppServerTestUI()
	child := &childUI.nativeSession
	child.Client.Live = sessioncontrol.Live{Thread: "child", NativeRoot: "main"}
	rootEndpoint := &nativeEndpoint{session: main, api: sessionapi.New()}
	childEndpoint := &nativeEndpoint{session: child, api: sessionapi.New()}
	n := &nativeConnection{root: main, endpoints: []*nativeEndpoint{rootEndpoint, childEndpoint}}
	family := &nativeFamily{threads: []binding.ThreadInfo{
		{ID: "main", AgentStatus: []byte(`"pending_init"`)},
		{ID: "child", ParentThreadID: "main", AgentStatus: []byte(`"completed"`)},
		{ID: "descendant", ParentThreadID: "child", AgentStatus: []byte(`"pending_init"`)},
		{ID: "sibling", ParentThreadID: "main", AgentStatus: []byte(`"running"`)},
	}}
	busy, blocked, work, _ := n.terminalExitWork(childEndpoint, family)
	if busy || blocked || len(work) != 1 || work[0].owner != "descendant" {
		t.Fatalf("child exit selected work outside its scope: %v %v", busy, work)
	}
	child.Client.Live.AwaitingTurn = true
	busy, blocked, work, _ = n.terminalExitWork(childEndpoint, family)
	if !busy || blocked || len(work) != 2 {
		t.Fatalf("exit missed newly admitted child input before turn start: %v %v", busy, work)
	}
	busy, blocked, work, _ = n.terminalExitWork(rootEndpoint, family)
	if busy || blocked || len(work) != 3 {
		t.Fatalf("idle Main did not see all affected work: %v %v", busy, work)
	}
	child.Client.Live.AwaitingTurn, child.Client.Live.NativeInputPending = false, true
	if busy, _, _, _ := n.terminalExitWork(childEndpoint, family); busy {
		t.Fatal("a retained-only thread's input capability was mistaken for execution")
	}
	child.attached.conversation.Unsent = []conversationmodel.Input{{Origin: "editor", Delivery: 1}}
	_, _, offered, _ := n.terminalExitWork(childEndpoint, family)
	child.attached.conversation.Unsent[0].Delivery++
	_, _, current, _ := n.terminalExitWork(childEndpoint, family)
	if offered[len(offered)-1] == current[len(current)-1] {
		t.Fatal("a newer waiting input was mistaken for work already offered")
	}
	n.families = map[string]*nativeFamily{"old-root": {threads: []binding.ThreadInfo{
		{ID: "old-child", ParentThreadID: "old-root", AgentStatus: []byte(`"running"`)},
	}}}
	if _, blocked, _, _ := n.terminalExitWork(rootEndpoint, family); !blocked {
		t.Fatal("Main exit could stop a different active family on the same host")
	}
}

func TestNativeDetachDuringWork(t *testing.T) {
	for _, command := range []string{"/detach", "/quit"} {
		t.Run(command, func(t *testing.T) {
			u, _ := newAppServerTestUI()
			u.startSession("main", "/workspace")
			c := nativeTestPromptClient(u)
			c.event.Frame.View.InterruptAvailable = true
			c.editor("main").InsertText(command)
			c.calls = nil
			c.composer(terminalsession.ComposerInput{Command: sessionpresentation.ComposerIntent{Operation: sessionpresentation.SubmitComposer}})
			if c.editor("main").Text != "" {
				t.Fatal("exit retained its command as a draft")
			}
			if command == "/detach" {
				if !c.quit || len(c.calls) != 0 {
					t.Fatal("explicit detach contacted the runtime")
				}
			} else if c.quit || !nativeTestExitRequested(c) {
				t.Fatal("ordinary exit did not ask the runtime before closing")
			}
		})
	}
}

// Ctrl-C clears a draft before it interrupts work or exits.
func TestNativeCtrlCClearsDraftFirst(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.startSession("main", "/workspace")
	c := nativeTestPromptClient(u)
	c.event.Frame.View.InterruptAvailable = true
	c.editor("main").InsertText("keep me")
	c.calls = nil
	c.dispatch(terminalsession.Input{Kind: terminalsession.KeyInput, Text: "\x03"})
	if c.editor("main").Text != "" || len(c.calls) != 0 || c.quit {
		t.Fatalf("first Ctrl-C: draft=%q calls=%d quit=%v", c.editor("main").Text, len(c.calls), c.quit)
	}
	c.dispatch(terminalsession.Input{Kind: terminalsession.KeyInput, Text: "\x03"})
	if len(c.calls) != 1 || c.calls[0].operation.Interrupt == nil {
		t.Fatal("second Ctrl-C did not interrupt the running turn")
	}
}

func TestNativeTerminalExitMenuWorksWithReadOnlyAdmission(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.startSession("main", "/workspace")
	c := nativeTestPromptClient(u)
	c.event.Frame.View.Admission.ReadOnly = true
	c.event.Frame.View.InterruptAvailable = true
	c.event.Frame.View.Draft.Available = true
	view := &nativeSessionView{picker: u.picker}
	view.offerTerminalExit([]nativeExitWork{{owner: "descendant"}})
	c.event.Frame.View.Menu = view.picker.menuView("", "", &u.Client.Live)
	c.refreshView(presentationTime(u.clock))
	c.calls = nil
	c.dispatch(terminalsession.Input{Kind: terminalsession.KeyInput, Text: "\x1b"})
	if len(c.calls) != 1 || c.calls[0].operation.Menu == nil || c.calls[0].operation.Menu.Kind != sessionpresentation.MenuDismiss {
		t.Fatal("Escape should dismiss the exit prompt, not interrupt work")
	}
	c.calls = nil
	nativeTestInput(c, "\x1b[B\r")
	if len(c.calls) != 1 || c.calls[0].operation.Menu == nil || c.calls[0].operation.Menu.Choice.ID != exitStopChoice {
		t.Fatal("a read-only admission blocked the selected exit action")
	}
}

func TestUISnapshotNativeTerminalExit(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.Turn = "running"
	u.shell.Transcript.Painter.Theme = diffrender.DarkTheme
	view := &nativeSessionView{picker: u.picker}
	view.offerTerminalExit([]nativeExitWork{{owner: "main"}})
	u.picker = view.picker
	rows, _ := u.testMainFrame(80, 18, 0)
	uisnapshot.AssertTerminal(t, "testdata/snapshots/native-terminal-exit.txt", rows, 80)

	legacy := u.attached.presentation.View()
	menu := *legacy.Menu
	menu.Dialog = true // A client can attach to a server from before quit was inline.
	legacy.Menu = &menu
	u.shell.Main.Update(legacy)
	got, _ := u.mainFrame(80, 18, 0)
	if !slices.Equal(got, rows) {
		t.Fatalf("older server drew the quit prompt differently:\n%q", got)
	}
}

func TestNativeDetachPresetWaitsForSubmission(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.startSession("main", "/workspace")
	c := nativeTestPromptClient(u)
	c.editor("main").InsertText("keep draft")
	c.calls = nil
	c.event.Frame.Effects = []sessionapi.NativeEffect{{ID: 1, Thread: "main", Recall: new("/detach")}}
	c.applyEffects()
	if c.quit || c.editor("main").Text != "/detach" {
		t.Fatal("preset detach did not wait for explicit submission")
	}
	for _, call := range c.calls {
		if call.operation.Receipt == nil {
			t.Fatal("preset detach submitted host work")
		}
	}
	c.calls = nil
	c.composer(terminalsession.ComposerInput{Command: sessionpresentation.ComposerIntent{Operation: sessionpresentation.SubmitComposer}})
	if !c.quit || c.editor("main").Text != "" || len(c.calls) != 0 {
		t.Fatal("explicitly submitted preset did not detach locally")
	}
}
