package router

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

func TestCommandSegmentsRetainRealShellResultsAcrossRestart(t *testing.T) {
	shell := newExecTrackShell(t)
	workspace := t.TempDir()
	storeDirectory := t.TempDir()
	store, err := openMekugiReplayStore(storeDirectory)
	if err != nil {
		t.Fatal(err)
	}
	u := newAppServerSessionTestUI(t, workspace)
	u.execTrack = shell.hub
	script := "printf 'first\\n'; printf 'second\\n' >&2; false && echo never"
	item := appServerItem{ID: "command", Type: "commandExecution", Command: "/usr/bin/bash -lc " + quoteShellWord(script), Status: "inProgress"}
	key := [3]string{"main", "turn", item.ID}
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": key[0], "turnId": key[1], "item": item})
	result := runExecTrackShell(t, append(shell.env, "CODEX_THREAD_ID=main"), script)
	if result.stdout != "first\n" || result.stderr != "second\n" || result.code != 1 {
		t.Fatalf("stock command changed: %+v", result)
	}
	if view := shell.awaitView(t, key); !view.complete {
		t.Fatal("shell did not report real boundaries")
	}
	u.proxy = &mekugiProxy{replayStore: store}
	item.Status, item.ExitCode, item.AggregatedOutput = "failed", new(1), new(result.stdout+result.stderr)
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": key[0], "turnId": key[1], "item": item})
	awaitMain(t, u, "skipped")
	// Reopen the durable owner and destroy the live report before restoring.
	store, err = openMekugiReplayStore(storeDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if shell.hub.tracking(key) {
		t.Fatal("test still has a live report")
	}
	for _, target := range []string{"main", "fork", "child"} {
		t.Run(target, func(t *testing.T) {
			restored := newAppServerSessionTestUI(t, workspace)
			restored.proxy = &mekugiProxy{replayStore: store}
			turns := []appServerHistoryTurn{{ID: key[1], Status: "completed", Items: []appServerItem{item}}}
			view := restored.view
			if target == "child" {
				restored.session.path(target)
				restored.restoreActivityThread(appServerThreadInfo{ID: target, Cwd: workspace, Turns: turns})
				view = restored.agents
			} else {
				restored.thread = target
				restored.restoreHistory(turns)
			}
			var entry activityPaneEntry
			for _, candidate := range view.entries {
				if candidate.CallID == item.ID && candidate.native != nil {
					entry = candidate
					break
				}
			}
			if entry.native == nil || len(entry.native.segments) != 4 {
				t.Fatalf("missing restored segments: %+v", entry)
			}
			segments := entry.native.segments
			for i, want := range []string{"first", "second", ""} {
				got := segments[i].output.View()
				if !got.Done || !got.Exited || strings.Join(got.Lines, "\n") != want || segments[i].running {
					t.Fatalf("segment %d lost output/state: %+v", i, got)
				}
			}
			if segments[2].exit != 1 || !segments[3].skipped || segments[3].output != nil {
				t.Fatal("failure/skipped state lost")
			}
			pages := view.commandOutputPages(entry.Seq)
			if len(pages) != 4 || pages[0].Output == pages[1].Output {
				t.Fatalf("per-command pages lost: %+v", pages)
			}
			catalog, err := store.readRetainedSession(storageSessionName(target))
			if err != nil || !catalog.Files[replayRecordName(workspace, commandSegmentsID(key[1], key[2]), false)] {
				t.Fatalf("restored owner not retained: %+v, %v", catalog, err)
			}
		})
	}
	for _, mismatch := range []string{"workspace", "turn", "item", "command", "output", "exit"} {
		t.Run(mismatch, func(t *testing.T) {
			restored := newAppServerSessionTestUI(t, workspace)
			restored.proxy = &mekugiProxy{replayStore: store}
			copy := item
			entry := activityPaneEntry{native: &liveActivityNativeItem{thread: "unrelated", turn: key[1], item: key[2]}}
			scope := workspace
			switch mismatch {
			case "workspace":
				scope = filepath.Join(workspace, "other")
			case "turn":
				entry.native.turn = "other"
			case "item":
				entry.native.item = "other"
			case "command":
				copy.Command = "/bin/bash -lc " + quoteShellWord(script)
			case "output":
				copy.AggregatedOutput = new("different")
			case "exit":
				copy.ExitCode = new(0)
			}
			restored.restoreCommandSegments(&entry, copy, scope)
			if len(entry.native.segments) != 0 {
				t.Fatal("borrowed a mismatched invocation's report")
			}
		})
	}
}

func TestCommandSegmentsUnavailableEvidenceKeepsCombinedOutput(t *testing.T) {
	for _, mode := range []string{"lossy", "released", "truncated", "long-line", "incomplete", "wrong-exit", "storage-failure", "corrupt", "missing"} {
		t.Run(mode, func(t *testing.T) {
			workspace := t.TempDir()
			store, err := openMekugiReplayStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			u := newAppServerSessionTestUI(t, workspace)
			u.proxy = &mekugiProxy{replayStore: store}
			item := appServerItem{ID: "cmd", Type: "commandExecution", Command: "bash -lc 'echo one; false'", AggregatedOutput: new("one\n"), ExitCode: new(1)}
			entry := activityPaneEntry{Seq: 1, Text: "Run `echo one`\n\nRun `false`", native: &liveActivityNativeItem{thread: "main", turn: "t", item: item.ID, command: item.Command}}
			first, second := u.session.outputs.New(), u.session.outputs.New()
			if mode == "truncated" {
				first.Write(strings.Repeat("line\n", activityui.OutputBytes/5))
			} else if mode == "long-line" {
				first.Write(strings.Repeat("x", activityui.OutputLineBytes+1))
			} else {
				first.Write("one\n")
			}
			first.Finish(nil, new(0))
			second.Finish(nil, new(1))
			view := execTrackView{complete: true, output: true, code: 1, segments: []commandSegment{{output: first}, {output: second, exit: 1}}}
			switch mode {
			case "lossy":
				view.output = false
			case "released":
				first.Release()
			case "incomplete":
				view.complete = false
			case "wrong-exit":
				view.code = 0
			case "storage-failure":
				store.maxBytes = 1
			}
			if mode != "missing" {
				u.retainCommandSegments(entry, item, view)
			}
			if mode == "storage-failure" && !strings.Contains(u.notice, "could not be retained") {
				t.Fatal("storage failure claimed success")
			}
			if mode == "corrupt" {
				path := filepath.Join(store.directory, replayRecordName(workspace, commandSegmentsID("t", item.ID), false))
				if err := os.WriteFile(path, []byte("broken"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			u.session.retainOutput(entry.native, item)
			u.restoreCommandSegments(&entry, item, workspace)
			u.view.entries = []activityPaneEntry{entry}
			pages := u.view.commandOutputPages(1)
			if len(pages) != 1 || pages[0].Label != "combined output" || pages[0].Output.View().Exit != 1 || strings.Join(pages[0].Output.View().Lines, "\n") != "one" {
				t.Fatalf("missing evidence fabricated output: %+v", pages)
			}
			wantStates := mode == "lossy" || mode == "released" || mode == "truncated" || mode == "long-line"
			if (len(entry.native.segments) == 2) != wantStates {
				t.Fatalf("wrong retained states: %+v", entry.native.segments)
			}
			if wantStates && entry.native.segments[1].exit != 1 {
				t.Fatal("lost observed segment failure")
			}
		})
	}
}

func TestCombinedOutputDialogUnwrapsOnlyLiteralShell(t *testing.T) {
	for _, command := range []string{
		`/usr/bin/bash -lc "mcat a.go; mcat b.go"`,
		`/usr/bin/bash -lc 'mcat a.go; mcat b.go' > output`,
	} {
		view := newLiveActivityView()
		view.entries = []activityPaneEntry{{Seq: 1, Text: "Read `a.go`\n\nRead `b.go`", native: &liveActivityNativeItem{command: command}}}
		pages := view.commandOutputPages(1)
		if len(pages) != 1 || pages[0].Code != appServerDisplayCommand(command) || view.entries[0].native.command != command {
			t.Fatalf("combined display changed execution or leaked wrapper: %+v", pages)
		}
		ui := &terminalUI{output: &outputDialog{view: view, origins: pages, pages: pages, match: -1}}
		ui.output.showPage(0)
		frame := drawOutputDialog(ui)
		if strings.Contains(frame, "/usr/bin/bash") != strings.Contains(command, "> output") || !strings.Contains(frame, "combined output") {
			t.Fatalf("wrong rendered shell display: %s", frame)
		}
	}
}

func TestCommandSegmentsRestoreRichChangeRows(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	u.proxy = &mekugiProxy{replayStore: store}
	listing := "amber1..amber5 +343 -658\namber6 +158 -81\n"
	item := appServerItem{ID: "cmd", Type: "commandExecution", Command: "bash -lc 'echo before; mchanges --list'", ExitCode: new(0), AggregatedOutput: new("before\n" + listing)}
	entry := activityPaneEntry{native: &liveActivityNativeItem{thread: "main", turn: "t", item: item.ID}}
	first, second := u.session.outputs.New(), u.session.outputs.New()
	first.Finish(new("before"), new(0))
	second.Finish(&listing, new(0))
	u.retainCommandSegments(entry, item, execTrackView{complete: true, output: true, segments: []commandSegment{{output: first}, {output: second}}})
	u.restoreHistory([]appServerHistoryTurn{{ID: "t", Status: "completed", Items: []appServerItem{item}}})
	// History starts collapsed; inspect the expanded retained command.
	var rows []string
	for _, restored := range u.view.entries {
		for _, block := range commandSegmentBlocks(restored) {
			block.Collapsed = false
			rows = append(rows, u.view.painter.Block(block, 90)...)
		}
	}
	frame := ansi.Strip(strings.Join(rows, "\n"))
	if !strings.Contains(frame, "amber1..amber5  +343 -658") || strings.Contains(frame, "┆ amber") {
		t.Fatalf("restored listing lost rich change rows:\n%s", frame)
	}
}
