package router

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestActivityAbsenceKeepsHostOutput(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.session.activity = nil
	u.view.conversation = true
	item := appServerItem{ID: "command", Type: "commandExecution", Command: "printf 'host output'", Status: "inProgress"}
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "turn", "item": item})
	appServerTestNotify(t, u, "item/commandExecution/outputDelta", map[string]any{"threadId": "main", "turnId": "turn", "itemId": item.ID, "delta": "host output\n"})
	awaitMain(t, u, "host output")
	item.Status, item.ExitCode, item.AggregatedOutput = "failed", new(3), new("host output\n")
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "turn", "item": item})
	awaitMain(t, u, "exit 3")
	appServerTestNotify(t, u, "hook/completed", map[string]any{"threadId": "main", "run": hookTestRun(u.session.cwd, "failed")})
	hookShown := false
	for _, entry := range u.view.entries {
		for _, block := range entry.blocks {
			if block.Output != nil {
				t.Fatal("absent activity retained output")
			}
			if block.Hook != nil {
				hookShown = true
				page := u.view.painter.DialogPage(block, 80)
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
	p := newUIReplayPlayback(nil, t.Context(), replayPlaybackTestSource(), 1)
	defer p.close()
	if err := p.seek(p.until); err != nil {
		t.Fatal(err)
	}
	if frame := replayPlaybackTestPaint(t, p, 120, 28); !strings.Contains(frame, "Repaint diagnosis complete") {
		t.Fatalf("absent activity blocked replay: %s", frame)
	}
	for _, entry := range p.ui.view.entries {
		if entry.native != nil && entry.native.output != nil {
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
				u.session.activity = nil
			}
			item := appServerItem{ID: "command", Type: "commandExecution", Command: "go test ./...", Status: "failed", AggregatedOutput: new("interrupted\n")}
			appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "turn", "item": item})
			found := false
			for _, entry := range u.view.entries {
				for _, block := range entry.blocks {
					if block.Verb != "Run" {
						continue
					}
					found = true
					page := u.view.painter.DialogPage(block, 80)
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
			u.view.conversation = true
			if absent {
				u.session.activity = nil
			}
			item := appServerItem{ID: "command", Type: "commandExecution", Command: "go test ./...", Status: "inProgress"}
			notify := func(method string, extra map[string]any) {
				extra["threadId"] = "main"
				extra["turnId"] = "turn"
				appServerTestNotify(t, u, method, extra)
			}
			notify("item/started", map[string]any{"item": item})
			notify("item/commandExecution/outputDelta", map[string]any{"itemId": item.ID, "delta": "first\n"})
			awaitMain(t, u, "first")
			var seq uint64
			for _, entry := range u.view.entries {
				if entry.native != nil && entry.native.item == item.ID {
					seq = entry.Seq
					break
				}
			}
			if !u.shell.openEntry(u.view, seq) {
				t.Fatal("no command dialog")
			}
			dialog := u.shell.output
			dialog.layout(76)
			if !strings.Contains(dialog.laid.Text, "first") {
				t.Fatalf("initial output missing: %q", dialog.laid.Text)
			}
			notify("item/commandExecution/outputDelta", map[string]any{"itemId": item.ID, "delta": "second\n"})
			awaitMain(t, u, "second")
			dialog.layout(76)
			if !strings.Contains(dialog.laid.Text, "second") {
				t.Errorf("dialog ignored later bounded tail: %q", dialog.laid.Text)
			}
			item.Status, item.ExitCode, item.AggregatedOutput = "failed", new(7), new("aggregate\n")
			notify("item/completed", map[string]any{"item": item})
			awaitMain(t, u, "exit 7")
			dialog.layout(76)
			if dialog.laid.Live || !strings.Contains(dialog.laid.Text, "aggregate") {
				t.Errorf("dialog ignored host completion: live=%v text=%q", dialog.laid.Live, dialog.laid.Text)
			}
		})
	}
}
