package router

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/backends/codex/binding"
)

func TestActivityAbsenceKeepsHostOutput(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.activity = nil
	u.view.Conversation = true
	u.testActivity(u.view).testAudience()
	item := binding.ThreadItem{ID: "command", Type: "commandExecution", Command: "printf 'host output'", Status: "inProgress"}
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "turn", "item": item})
	appServerTestNotify(t, u, "item/commandExecution/outputDelta", map[string]any{"threadId": "main", "turnId": "turn", "itemId": item.ID, "delta": "host output\n"})
	awaitMain(t, u, "host output")
	item.Status, item.ExitCode, item.AggregatedOutput = "failed", new(3), new("host output\n")
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "turn", "item": item})
	awaitMain(t, u, "exit 3")
	appServerTestNotify(t, u, "hook/completed", map[string]any{"threadId": "main", "run": hookTestRun(u.cwd, "failed")})
	hookShown := false
	for _, entry := range u.source.Entries {
		for _, block := range u.view.Disclosure.Blocks(entry.Values) {
			if block.Output != 0 {
				t.Fatal("absent activity retained output")
			}
			if block.Hook != nil {
				hookShown = true
				page := u.view.Painter.DialogPage(block, 80)
				if !strings.Contains(page.Text, "policy checked") || strings.Contains(ansi.Strip(page.Detail), "exit") {
					t.Fatalf("hook fallback lost output or invented a shell exit: %+v", page)
				}
			}
		}
	}
	if !hookShown {
		t.Fatal("absent activity hid the hook result")
	}
}

func TestActivityAbsenceKeepsOfflineReplay(t *testing.T) {
	p := newTestUIReplayPlayback(nil, t.Context(), replayPlaybackTestSource(), 1)
	defer p.close()
	if err := p.seek(p.Until); err != nil {
		t.Fatal(err)
	}
	if frame := replayPlaybackTestPaint(t, p, 120, 28); !strings.Contains(frame, "Repaint diagnosis complete") {
		t.Fatalf("absent activity blocked replay: %s", frame)
	}
	for _, entry := range p.ui.source.Entries {
		if entry.Source.Native != nil && entry.Source.Native.Output != nil {
			t.Fatal("offline replay retained output without activity")
		}
	}
}

func TestActivityAbsenceUnknownExit(t *testing.T) {
	for _, absent := range []bool{false, true} {
		name := "present"
		if absent {
			name = "absent"
		}
		t.Run(name, func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			if absent {
				u.activity = nil
			}
			item := binding.ThreadItem{ID: "command", Type: "commandExecution", Command: "go test ./...", Status: "failed", AggregatedOutput: new("interrupted\n")}
			appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "turn", "item": item})
			found := false
			for _, entry := range u.source.Entries {
				for _, block := range u.view.Disclosure.Blocks(entry.Values) {
					if block.Verb != "Run" {
						continue
					}
					found = true
					page := u.view.Painter.DialogPage(block, 80)
					if !strings.Contains(page.Text, "interrupted") {
						t.Errorf("unknown exit discarded host output: %q", page.Text)
					}
					if strings.Contains(ansi.Strip(page.Detail), "exit 0") {
						t.Errorf("unknown host exit is displayed as success: %q", ansi.Strip(page.Detail))
					}
				}
			}
			if !found {
				t.Fatal("command Run block not found")
			}
		})
	}
}

func TestActivityAbsenceDialogRefresh(t *testing.T) {
	for _, absent := range []bool{false, true} {
		name := "present"
		if absent {
			name = "absent"
		}
		t.Run(name, func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			u.view.Conversation = true
			u.testActivity(u.view).testAudience()
			if absent {
				u.activity = nil
			}
			item := binding.ThreadItem{ID: "command", Type: "commandExecution", Command: "go test ./...", Status: "inProgress"}
			notify := func(method string, extra map[string]any) {
				extra["threadId"] = "main"
				extra["turnId"] = "turn"
				appServerTestNotify(t, u, method, extra)
			}
			notify("item/started", map[string]any{"item": item})
			notify("item/commandExecution/outputDelta", map[string]any{"itemId": item.ID, "delta": "first\n"})
			awaitMain(t, u, "first")
			var seq uint64
			for _, entry := range u.source.Entries {
				if entry.Source.Native != nil && entry.Source.Native.Item == item.ID {
					seq = entry.Source.Seq
					break
				}
			}
			if !u.shell.openEntry(u.view, seq) {
				t.Fatal("no command dialog")
			}
			dialog := u.shell.output
			dialog.layout(76)
			if !strings.Contains(dialog.Document.Text, "first") {
				t.Fatalf("initial output missing: %q", dialog.Document.Text)
			}
			notify("item/commandExecution/outputDelta", map[string]any{"itemId": item.ID, "delta": "second\n"})
			awaitMain(t, u, "second")
			dialog.layout(76)
			if !strings.Contains(dialog.Document.Text, "second") {
				t.Errorf("dialog ignored later bounded tail: %q", dialog.Document.Text)
			}
			item.Status, item.ExitCode, item.AggregatedOutput = "failed", new(7), new("aggregate\n")
			notify("item/completed", map[string]any{"item": item})
			awaitMain(t, u, "exit 7")
			dialog.layout(76)
			if dialog.Document.Live || !strings.Contains(dialog.Document.Text, "aggregate") {
				t.Errorf("dialog ignored host completion: live=%v text=%q", dialog.Document.Live, dialog.Document.Text)
			}
		})
	}
}
