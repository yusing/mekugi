package launch

import (
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/backends/codex/binding"
)

func TestAppServerProgressCompactionLifecycle(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	now := time.Date(2026, 10, 29, 9, 0, 0, 0, time.UTC)
	u.clock = func() time.Time { return now }
	appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "main", "turn": map[string]any{"id": "t"}})
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": map[string]any{"id": "q", "type": "userMessage", "content": []any{map[string]any{"type": "text", "text": "Do the work"}}}})
	item := func(thread, turn, id, method string) {
		t.Helper()
		appServerTestNotify(t, u, method, map[string]any{"threadId": thread, "turnId": turn, "item": map[string]any{"id": id, "type": "contextCompaction"}})
	}
	label := func(want string) {
		t.Helper()
		frame, _ := u.testMainFrame(80, 18, 0)
		if got := ansi.Strip(frame[u.shell.Main.ComposerRect.Y-1]); !strings.Contains(got, want) {
			t.Fatalf("composer = %q, want %q", got, want)
		}
	}
	workStarted := u.attached.presentation.Status.Started
	now = now.Add(40 * time.Minute)
	item("main", "t", "c", "item/started")
	assertElapsed := func(want string) {
		t.Helper()
		if got := ansi.Strip(u.sessionLabel(now)); !strings.HasSuffix(got, want) {
			t.Fatalf("status = %q, want suffix %q", got, want)
		}
	}
	assertElapsed("Compacting context")
	now = now.Add(2 * time.Minute)
	item("main", "t", "c", "item/started")
	assertElapsed("Compacting context 2m")
	label("Compacting context")
	item("child", "t", "c", "item/completed")
	item("main", "old", "c", "item/completed")
	item("main", "t", "other", "item/completed")
	label("Compacting context")
	item("main", "t", "c", "item/completed")
	label("Working")
	assertElapsed("Working 42m")
	if u.attached.presentation.Status.Started != workStarted {
		t.Fatal("compaction changed logical work start")
	}
	before := len(u.attached.source.Entries)
	item("main", "t", "c", "item/completed")
	item("main", "t", "c", "item/started")
	label("Working")
	assertElapsed("Working 42m")
	if len(u.attached.source.Entries) != before {
		t.Fatal("duplicate compaction completion created an event")
	}
	frame, _ := u.testMainFrame(80, 24, 0)
	screen := ansi.Strip(strings.Join(frame, "\n"))
	if !strings.Contains(screen, "Context compacted") || strings.Contains(screen, "re:") {
		t.Fatalf("compaction should be an event without reply context: %s", screen)
	}
	for _, status := range []string{"interrupted", "failed", "completed"} {
		appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "main", "turn": map[string]any{"id": status}})
		item("main", status, "pending", "item/started")
		assertElapsed("Compacting context")
		now = now.Add(time.Minute)
		assertElapsed("Compacting context 1m")
		before = len(u.attached.source.Entries)
		appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "main", "turn": map[string]any{"id": status, "status": status}})
		if strings.Contains(u.sessionLabel(time.Now()), "Compacting") || len(u.attached.source.Entries) != before {
			t.Fatal("turn end retained compaction or fabricated completion")
		}
	}
}

func TestAppServerProgressTerminalPolling(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "main", "turn": map[string]any{"id": "t"}})
	poll := func(thread, turn, process, stdin string) {
		t.Helper()
		appServerTestNotify(t, u, "item/commandExecution/terminalInteraction", map[string]any{"threadId": thread, "turnId": turn, "itemId": "cmd", "processId": process, "stdin": stdin})
	}
	label := func(want string) {
		t.Helper()
		frame, _ := u.testMainFrame(60, 12, 0)
		if got := ansi.Strip(frame[u.shell.Main.ComposerRect.Y-1]); !strings.Contains(got, want) {
			t.Fatalf("composer = %q, want %q", got, want)
		}
	}
	poll("child", "t", "proc", "")
	poll("main", "old", "proc", "")
	poll("main", "t", "proc", "input")
	label("Working")
	poll("main", "t", "proc", "")
	poll("main", "t", "proc", "")
	label("Still running")
	if len(u.attached.source.Entries) != 0 {
		t.Fatal("polls flooded transcript")
	}
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": binding.ThreadItem{ID: "other", Type: "commandExecution", ProcessID: "other"}})
	label("Still running")
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": binding.ThreadItem{ID: "cmd", Type: "commandExecution", ProcessID: "proc"}})
	label("Working")
	poll("main", "t", "proc", "")
	poll("main", "t", "proc", "input")
	label("Working")
	poll("main", "t", "proc", "")
	appServerTestNotify(t, u, "item/agentMessage/delta", map[string]any{"threadId": "main", "turnId": "t", "itemId": "a", "delta": "Continuing"})
	label("Working")
	poll("main", "t", "proc", "")
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "t", "item": binding.ThreadItem{ID: "compact", Type: "contextCompaction"}})
	poll("main", "t", "proc", "")
	label("Compacting context")
	appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "main", "turn": map[string]any{"id": "t", "status": "interrupted"}})
	if got := publishPresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side).Status.Session; got.Text != "Interrupted" || got.Running {
		t.Fatal("interrupt retained progress")
	}
}
