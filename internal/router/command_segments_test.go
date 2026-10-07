package router

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/gofrs/flock"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

func awaitCommandSegments(t *testing.T, u *appServerUI) {
	t.Helper()
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	for u.commandSegmentPending > 0 {
		select {
		case err := <-u.commandSegmentWrites:
			u.commandSegmentRetained(err)
		case <-timeout.C:
			t.Fatal("command segment retention did not finish")
		}
	}
}

func TestCommandSegmentsRetainAfterStorageContentionWithoutBlockingUI(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		workspace := t.TempDir()
		store, err := openMekugiReplayStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		u := newAppServerSessionTestUI(t, workspace)
		u.proxy = &mekugiProxy{replayStore: store}
		key := [3]string{"main", "turn", "command"}
		first, second := u.session.outputs.New(), u.session.outputs.New()
		first.Finish(new("first\n"), new(0))
		second.Finish(new("second\n"), new(0))
		u.execTrack = &execTrackHub{tracks: map[[3]string]*execTrack{key: {done: true, ended: true, segments: []execTrackSegment{
			{source: "echo first", began: true, ended: true, full: first},
			{source: "echo second", began: true, ended: true, full: second},
		}}}}
		item := appServerItem{ID: key[2], Type: "commandExecution", Command: "bash -lc 'echo first; echo second'", ExitCode: new(0), AggregatedOutput: new("first\nsecond\n")}
		entry := activityPaneEntry{native: &liveActivityNativeItem{thread: key[0], turn: key[1], item: key[2]}}
		lock := flock.New(filepath.Join(store.directory, "store.lock"))
		if err := lock.Lock(); err != nil {
			t.Fatal(err)
		}
		defer lock.Unlock()
		ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
		err = store.put(ctx, workspace, nil)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("shared-store contention did not produce the diagnosed deadline error: %v", err)
		}
		started := time.Now()
		done := u.trackedCommandDone(key, entry, item)
		if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
			t.Fatalf("storage contention blocked command presentation for %s", elapsed)
		}
		if len(done) != 1 || len(done[0].native.segments) != 2 || done[0].native.segments[0].output != first {
			t.Fatalf("live command completion lost its output: %+v", done)
		}
		// Switching sessions and releasing live buffers must not change the pending
		// report's evidence or ownership. Hold beyond the former one-second limit.
		u.session.cwd, u.thread = t.TempDir(), "other"
		first.Release()
		second.Release()
		time.Sleep(1100 * time.Millisecond)
		select {
		case result := <-u.commandSegmentWrites:
			u.commandSegmentRetained(result)
			t.Fatalf("report abandoned a temporary storage lock: %v", result.err)
		default:
		}
		if err := lock.Unlock(); err != nil {
			t.Fatal(err)
		}
		u.finishCommandSegments()
		if notices := u.issues.Pending(); len(notices) != 0 {
			t.Fatal(notices)
		}
		reopened, err := openMekugiReplayStore(store.directory)
		if err != nil {
			t.Fatal(err)
		}
		restored := newAppServerSessionTestUI(t, workspace)
		restored.proxy = &mekugiProxy{replayStore: reopened}
		entry.native = &liveActivityNativeItem{thread: "fork", turn: key[1], item: key[2]}
		restored.restoreCommandSegments(&entry, item, workspace)
		if len(entry.native.segments) != 2 {
			t.Fatalf("report not available after restart: %+v", entry.native)
		}
		for i, want := range []string{"first", "second"} {
			if got := strings.Join(entry.native.segments[i].output.View().Lines, "\n"); got != want {
				t.Fatalf("retained output %d = %q, want %q", i, got, want)
			}
		}
		catalog, err := reopened.readRetainedSession(storageSessionName(key[0]))
		if err != nil || !catalog.Files[replayRecordName(workspace, commandSegmentsID(key[1], key[2]), false)] {
			t.Fatalf("original thread did not retain its report: %+v, %v", catalog, err)
		}
		if _, err := reopened.readRetainedSession(storageSessionName("other")); !os.IsNotExist(err) {
			t.Fatalf("pending write borrowed switched thread ownership: %v", err)
		}
	})
}

func TestCommandSegmentsPendingRetentionIsBoundedAndCanceled(t *testing.T) {
	t.Parallel()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	u := newAppServerSessionTestUI(t, t.TempDir())
	ctx, cancel := context.WithCancel(t.Context())
	u.ctx = ctx
	defer cancel()
	u.proxy = &mekugiProxy{replayStore: store}
	lock := flock.New(filepath.Join(store.directory, "store.lock"))
	if err := lock.Lock(); err != nil {
		t.Fatal(err)
	}
	defer lock.Unlock()
	for i := range commandSegmentRetentionLimit + 1 {
		item := appServerItem{ID: fmt.Sprint(i), Type: "commandExecution", Command: "bash -lc 'true; true'", ExitCode: new(0), AggregatedOutput: new("")}
		entry := activityPaneEntry{native: &liveActivityNativeItem{thread: "main", turn: "turn", item: item.ID}}
		u.retainCommandSegments(entry, item, execTrackView{complete: true, segments: []commandSegment{{}, {}}})
	}
	if notices := u.issues.Pending(); u.commandSegmentPending != commandSegmentRetentionLimit || len(notices) != 1 || !strings.Contains(notices[0], "reached the limit of 32") {
		t.Fatalf("retention grew beyond its bound: %d, %v", u.commandSegmentPending, notices)
	}
	cancel()
	awaitCommandSegments(t, u)
	if notices := u.issues.Pending(); len(notices) != commandSegmentRetentionLimit+1 || !strings.Contains(notices[1], context.Canceled.Error()) {
		t.Fatalf("canceled retention lost its underlying error: %v", notices)
	}
	files, err := filepath.Glob(filepath.Join(store.directory, "call-*.json"))
	if err != nil || len(files) != 0 {
		t.Fatalf("canceled writes published records: %v, %v", files, err)
	}
}

func TestCommandSegmentsRetentionErrorsStayWithOriginalThreadAndCause(t *testing.T) {
	t.Parallel()
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.thread = "other"
	u.view.conversation = true
	first := errors.New("open replay record: permission denied; complete underlying cause")
	second := errors.New("sync replay directory: no space left on device; different underlying cause")
	for _, cause := range []error{first, second} {
		u.commandSegmentPending++
		u.commandSegmentRetained(commandSegmentWrite{key: [3]string{"main", "turn", "item"}, err: storageIOError(cause)})
	}
	if delivery := u.applyCriticalNotices(); delivery != nil || len(u.view.entries) != 0 {
		t.Fatal("switched thread borrowed another thread's retention errors")
	}
	u.thread = "main"
	delivery := u.applyCriticalNotices()

	if delivery == nil || len(u.view.entries) != 0 || !u.noticeAlert {
		t.Fatal("retention errors missed the composer")
	}
	for _, cause := range []error{first, second} {
		if !strings.Contains(u.notice, cause.Error()) || !strings.Contains(u.notice, "item item (turn turn)") {
			t.Fatal("composer omitted complete error or identity")
		}
	}
	before := u.notice
	delivery.finish(false)
	if retry := u.applyCriticalNotices(); retry == nil || u.notice != before || len(u.view.entries) != 0 {
		t.Fatal("unpainted error was lost or duplicated")
	} else {
		u.mainFrame(100, 12, 0)
		retry.finish(true)
	}
	if len(u.issues.Pending()) != 0 {
		t.Fatal("painted errors remained pending")
	}
}

func TestCommandSegmentsRetainRealShellResultsAcrossRestart(t *testing.T) {
	t.Parallel()
	shell := newExecTrackShell(t)
	workspace := t.TempDir()
	storeDirectory := t.TempDir()
	store, err := openMekugiReplayStore(storeDirectory)
	if err != nil {
		t.Fatal(err)
	}
	u := newAppServerSessionTestUI(t, workspace)
	u.execTrack = shell.hub
	path := filepath.Join(t.TempDir(), "SKILL.md")
	if err := os.WriteFile(path, []byte("first\n"), 0600); err != nil {
		t.Fatal(err)
	}
	script := "cat " + quoteShellWord(path) + "; printf 'second\\n' >&2; false && echo never"
	item := appServerItem{ID: "command", Type: "commandExecution", Command: "/usr/bin/bash -lc " + quoteShellWord(script), Status: "inProgress"}
	key := [3]string{"main", "turn", item.ID}
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": key[0], "turnId": key[1], "item": item})
	result := runExecTrackShell(t, append(shell.env, "CODEX_THREAD_ID=main"), script)
	if result.stdout != "first\n" || result.stderr != "second\n" || result.code != 1 {
		t.Fatalf("stock command changed: %+v", result)
	}
	u.proxy = &mekugiProxy{replayStore: store}
	item.Status, item.ExitCode, item.AggregatedOutput = "failed", new(1), new(result.stdout+result.stderr)
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": key[0], "turnId": key[1], "item": item})
	awaitMain(t, u, "skipped")
	awaitCommandSegments(t, u)
	if names := u.view.activeSkills()["Main"]; len(names) != 1 || names[0] != filepath.Base(filepath.Dir(path)) {
		t.Fatalf("successful skill segment lost after a failed command: %v", names)
	}
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
					entry = candidate.activityPaneEntry
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
			if names := view.activeSkills()[entry.Agent]; len(names) != 1 || names[0] != filepath.Base(filepath.Dir(path)) {
				t.Fatalf("restored successful skill segment lost: %v", names)
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
	t.Parallel()
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
				awaitCommandSegments(t, u)
			}
			if mode == "storage-failure" {
				notices := u.issues.Pending()
				if len(notices) != 1 || !strings.Contains(notices[0], "limit is 1 bytes") || !u.dirty {
					t.Fatalf("storage failure omitted its actual cause: %v", notices)
				}
			}
			if mode == "corrupt" {
				path := filepath.Join(store.directory, replayRecordName(workspace, commandSegmentsID("t", item.ID), false))
				if err := os.WriteFile(path, []byte("broken"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			u.session.retainOutput(entry.native, item)
			u.restoreCommandSegments(&entry, item, workspace)
			u.view.entries = []liveActivityRecord{{activityPaneEntry: entry}}
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
	t.Parallel()
	for _, command := range []string{
		`/usr/bin/bash -lc "mcat a.go; mcat b.go"`,
		`/usr/bin/bash -lc 'mcat a.go; mcat b.go' > output`,
	} {
		view := newLiveActivityView()
		view.entries = []liveActivityRecord{{Seq: 1, Text: "Read `a.go`\n\nRead `b.go`", native: &liveActivityNativeItem{command: command}}}
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
	t.Parallel()
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
	awaitCommandSegments(t, u)
	u.restoreHistory([]appServerHistoryTurn{{ID: "t", Status: "completed", Items: []appServerItem{item}}})
	// History starts collapsed; inspect the expanded retained command.
	var rows []string
	for _, restored := range u.view.entries {
		for _, block := range commandSegmentBlocks(restored.activityPaneEntry) {
			block.Collapsed = false
			rows = append(rows, u.view.painter.Block(block, 90)...)
		}
	}
	frame := ansi.Strip(strings.Join(rows, "\n"))
	if !strings.Contains(frame, "amber1..amber5  +343 -658") || strings.Contains(frame, "┆ amber") {
		t.Fatalf("restored listing lost rich change rows:\n%s", frame)
	}
}
