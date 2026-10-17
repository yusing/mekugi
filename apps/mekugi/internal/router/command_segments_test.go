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

	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"

	"github.com/yusing/mekugi/backends/codex/binding"

	activitymodel "github.com/yusing/mekugi/modules/activity/feed"

	"github.com/charmbracelet/x/ansi"
	"github.com/gofrs/flock"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	"github.com/yusing/mekugi/modules/commandsegments/reports"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

func awaitCommandSegments(t *testing.T, u *appServerUI) {
	t.Helper()
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	for u.commandSegmentPending > 0 {
		select {
		case err := <-u.commandSegmentWrites:
			commandSegmentRetained(&u.commandSegmentPending, u.issues, &u.dirty, err)
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
		setTestAppServerIntegration(u, testProxyWithStore(store))
		key := [3]string{"main", "turn", "command"}
		first, second := u.activity.Outputs(u.attached.session.Scope()).New(), u.activity.Outputs(u.attached.session.Scope()).New()
		first.Finish(new("first\n"), new(0))
		second.Finish(new("second\n"), new(0))
		view := execTrackView{complete: true, output: true, segments: []sessionpresentation.ActivitySegment{
			{Source: "echo first", Output: first}, {Source: "echo second", Output: second},
		}}
		item := binding.ThreadItem{ID: key[2], Type: "commandExecution", Command: "bash -lc 'echo first; echo second'", ExitCode: new(0), AggregatedOutput: new("first\nsecond\n")}
		entry := sessionpresentation.ActivityEntry{Native: &sessionpresentation.NativeItem{Thread: key[0], Turn: key[1], Item: key[2]}}
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
		retainCommandSegments(u.ctx, u.retained, u.segments, &u.attached, &u.commandSegmentWrites, &u.commandSegmentPending, u.issues, &u.dirty, entry, item.Observation(), view)
		if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
			t.Fatalf("storage contention blocked command presentation for %s", elapsed)
		}
		// Switching sessions and releasing live buffers must not change the pending
		// report's evidence or ownership. Hold beyond the former one-second limit.
		u.attached.cwd, u.Thread = t.TempDir(), "other"
		first.Release()
		second.Release()
		time.Sleep(1100 * time.Millisecond)
		select {
		case result := <-u.commandSegmentWrites:
			commandSegmentRetained(&u.commandSegmentPending, u.issues, &u.dirty, result)
			t.Fatalf("report abandoned a temporary storage lock: %v", result.err)
		default:
		}
		if err := lock.Unlock(); err != nil {
			t.Fatal(err)
		}
		finishCommandSegments(&u.commandSegmentWrites, &u.commandSegmentPending, u.issues, &u.dirty)
		if notices := u.issues.Notices.Pending(); len(notices) != 0 {
			t.Fatal(notices)
		}
		reopened, err := openMekugiReplayStore(store.directory)
		if err != nil {
			t.Fatal(err)
		}
		restored := newAppServerSessionTestUI(t, workspace)
		setTestAppServerIntegration(restored, testProxyWithStore(reopened))
		entry.Native = &sessionpresentation.NativeItem{Thread: "fork", Turn: key[1], Item: key[2]}
		restoreCommandSegments(restored.ctx, restored.retained, restored.segments, &restored.attached, restored.activity, restored.restoredSegments, &entry, item, workspace)
		if len(entry.Native.Segments) != 2 {
			t.Fatalf("report not available after restart: %+v", entry.Native)
		}
		for i, want := range []string{"first", "second"} {
			if got := strings.Join(entry.Native.Segments[i].Output.View().Lines, "\n"); got != want {
				t.Fatalf("retained output %d = %q, want %q", i, got, want)
			}
		}
		catalog, err := reopened.publication.ReadCatalog(history.SessionName(key[0]))
		if err != nil || !catalog.Files[reports.Name(workspace, reports.ID(key[1], key[2]))] {
			t.Fatalf("original thread did not retain its report: %+v, %v", catalog, err)
		}
		if _, err := reopened.publication.ReadCatalog(history.SessionName("other")); !os.IsNotExist(err) {
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
	setTestAppServerIntegration(u, testProxyWithStore(store))
	lock := flock.New(filepath.Join(store.directory, "store.lock"))
	if err := lock.Lock(); err != nil {
		t.Fatal(err)
	}
	defer lock.Unlock()
	for i := range commandSegmentRetentionLimit + 1 {
		item := binding.ThreadItem{ID: fmt.Sprint(i), Type: "commandExecution", Command: "bash -lc 'true; true'", ExitCode: new(0), AggregatedOutput: new("")}
		entry := sessionpresentation.ActivityEntry{Native: &sessionpresentation.NativeItem{Thread: "main", Turn: "turn", Item: item.ID}}
		retainCommandSegments(u.ctx, u.retained, u.segments, &u.attached, &u.commandSegmentWrites, &u.commandSegmentPending, u.issues, &u.dirty, entry, item.Observation(), execTrackView{complete: true, segments: []sessionpresentation.ActivitySegment{{}, {}}})
	}
	if notices := u.issues.Notices.Pending(); u.commandSegmentPending != commandSegmentRetentionLimit || len(notices) != 1 || !strings.Contains(notices[0], "reached the limit of 32") {
		t.Fatalf("retention grew beyond its bound: %d, %v", u.commandSegmentPending, notices)
	}
	cancel()
	awaitCommandSegments(t, u)
	if notices := u.issues.Notices.Pending(); len(notices) != commandSegmentRetentionLimit+1 || !strings.Contains(notices[1], context.Canceled.Error()) {
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
	u.Thread = "other"
	u.shell.Transcript.Conversation = true
	u.testActivity(u.shell.Transcript).testAudience()
	first := errors.New("open replay record: permission denied; complete underlying cause")
	second := errors.New("sync replay directory: no space left on device; different underlying cause")
	for _, cause := range []error{first, second} {
		u.commandSegmentPending++
		commandSegmentRetained(&u.commandSegmentPending, u.issues, &u.dirty, commandSegmentWrite{key: [3]string{"main", "turn", "item"}, err: requestexecution.StorageIOError(cause)})
	}
	if delivery := u.applyCriticalNotices(); delivery != nil || len(u.attached.source.Entries) != 0 {
		t.Fatal("switched thread borrowed another thread's retention errors")
	}
	u.Thread = "main"
	delivery := u.applyCriticalNotices()

	if delivery == nil || len(u.attached.source.Entries) != 0 || !u.attached.feedback.Alert {
		t.Fatal("retention errors missed the composer")
	}
	for _, cause := range []error{first, second} {
		if !strings.Contains(u.attached.feedback.Text, cause.Error()) || !strings.Contains(u.attached.feedback.Text, "item item (turn turn)") {
			t.Fatal("composer omitted complete error or identity")
		}
	}
	before := u.attached.feedback.Text
	delivery.finish(terminalsession.NoticeReceipt{})
	if retry := u.applyCriticalNotices(); retry == nil || u.attached.feedback.Text != before || len(u.attached.source.Entries) != 0 {
		t.Fatal("unpainted error was lost or duplicated")
	} else {
		_, receipt := u.testDeliveredMainFrame(t, 100, 12, 0)
		retry.finish(receipt)
	}
	if len(u.issues.Notices.Pending()) != 0 {
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
	u.execTrack = shell.hub.Observer
	shell.hub.ui = u
	path := filepath.Join(t.TempDir(), "SKILL.md")
	if err := os.WriteFile(path, []byte("first\n"), 0600); err != nil {
		t.Fatal(err)
	}
	script := "cat " + quoteShellWord(path) + "; printf 'second\\n' >&2; false && echo never"
	item := binding.ThreadItem{ID: "command", Type: "commandExecution", Command: "/usr/bin/bash -lc " + quoteShellWord(script), Status: "inProgress"}
	key := [3]string{"main", "turn", item.ID}
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": key[0], "turnId": key[1], "item": item})
	result := runExecTrackShell(t, append(shell.env, "CODEX_THREAD_ID=main"), script)
	if result.stdout != "first\n" || result.stderr != "second\n" || result.code != 1 {
		t.Fatalf("stock command changed: %+v", result)
	}
	setTestAppServerIntegration(u, testProxyWithStore(store))
	item.Status, item.ExitCode, item.AggregatedOutput = "failed", new(1), new(result.stdout+result.stderr)
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": key[0], "turnId": key[1], "item": item})
	awaitMain(t, u, "skipped")
	awaitCommandSegments(t, u)
	if names := u.attached.source.ActiveSkills()["Main"]; len(names) != 1 || names[0] != filepath.Base(filepath.Dir(path)) {
		t.Fatalf("successful skill segment lost after a failed command: %v", names)
	}
	// Reopen the durable owner and destroy the live report before restoring.
	store, err = openMekugiReplayStore(storeDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if shell.hub.Tracking(key) {
		t.Fatal("test still has a live report")
	}
	for _, target := range []string{"main", "fork", "child", "child-other-workspace", "child-missing-workspace"} {
		t.Run(target, func(t *testing.T) {
			restored := newAppServerSessionTestUI(t, workspace)
			setTestAppServerIntegration(restored, testProxyWithStore(store))
			turns := []binding.HistoryTurn{{ID: key[1], Status: "completed", Items: []binding.ThreadItem{item}}}
			view := restored.testActivity(restored.shell.Transcript)
			if strings.HasPrefix(target, "child") {
				childWorkspace := workspace
				if target == "child-other-workspace" {
					restored.attached.cwd = t.TempDir()
				} else if target == "child-missing-workspace" {
					childWorkspace = ""
				}
				restored.attached.session.Caller(target)
				restored.restoreActivityThread(binding.ThreadInfo{ID: target, Cwd: childWorkspace, Turns: turns}, false)
				view = restored.testActivity(restored.shell.Agents)
			} else {
				restored.Thread = target
				restored.restoreHistory(turns)
			}
			var entry sessionpresentation.ActivityEntry
			for _, candidate := range view.Entries {
				if candidate.Source.CallID == item.ID && candidate.Source.Native != nil {
					entry = candidate.Source
					break
				}
			}
			if entry.Native == nil {
				t.Fatal("missing restored command")
			}
			if target == "child-missing-workspace" {
				pages := activityui.Disclosure(nil).Blocks(sessionpresentation.CommandPresentation(entry).DetailValues())
				if len(entry.Native.Segments) != 0 || len(pages) != 1 || pages[0].Label != "combined output" || strings.Join(view.RetainedOutput(pages[0].Source, pages[0].Output).View().Lines, "\n") != "first\nsecond" {
					t.Fatalf("missing workspace borrowed a report or lost combined output: %+v", entry)
				}
				if _, err := store.publication.ReadCatalog(history.SessionName(target)); !os.IsNotExist(err) {
					t.Fatalf("missing workspace adopted a report: %v", err)
				}
				return
			}
			if len(entry.Native.Segments) != 4 {
				t.Fatalf("missing restored segments: %+v", entry)
			}
			segments := entry.Native.Segments
			for i, want := range []string{"first", "second", ""} {
				got := segments[i].Output.View()
				if !got.Done || !got.Exited || strings.Join(got.Lines, "\n") != want || segments[i].Running {
					t.Fatalf("segment %d lost output/state: %+v", i, got)
				}
			}
			if segments[2].Exit != 1 || !segments[3].Skipped || segments[3].Output != nil {
				t.Fatal("failure/skipped state lost")
			}
			if names := view.ActiveSkills()[entry.Agent]; len(names) != 1 || names[0] != filepath.Base(filepath.Dir(path)) {
				t.Fatalf("restored successful skill segment lost: %v", names)
			}
			pages := activityui.Disclosure(nil).Blocks(sessionpresentation.CommandPresentation(entry).DetailValues())
			if len(pages) != 4 || pages[0].Output == pages[1].Output {
				t.Fatalf("per-command pages lost: %+v", pages)
			}
			catalog, err := store.publication.ReadCatalog(history.SessionName(target))
			if err != nil || !catalog.Files[reports.Name(workspace, reports.ID(key[1], key[2]))] {
				t.Fatalf("restored owner not retained: %+v, %v", catalog, err)
			}
		})
	}
	for _, mismatch := range []string{"workspace", "turn", "item", "command", "output", "exit"} {
		t.Run(mismatch, func(t *testing.T) {
			restored := newAppServerSessionTestUI(t, workspace)
			setTestAppServerIntegration(restored, testProxyWithStore(store))
			copy := item
			entry := sessionpresentation.ActivityEntry{Native: &sessionpresentation.NativeItem{Thread: "unrelated", Turn: key[1], Item: key[2]}}
			scope := workspace
			switch mismatch {
			case "workspace":
				scope = filepath.Join(workspace, "other")
			case "turn":
				entry.Native.Turn = "other"
			case "item":
				entry.Native.Item = "other"
			case "command":
				copy.Command = "/bin/bash -lc " + quoteShellWord(script)
			case "output":
				copy.AggregatedOutput = new("different")
			case "exit":
				copy.ExitCode = new(0)
			}
			restoreCommandSegments(restored.ctx, restored.retained, restored.segments, &restored.attached, restored.activity, restored.restoredSegments, &entry, copy, scope)
			if len(entry.Native.Segments) != 0 {
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
			if mode == "storage-failure" {
				store.maxBytes = 1
			}
			u := newAppServerSessionTestUI(t, workspace)
			setTestAppServerIntegration(u, testProxyWithStore(store))
			item := binding.ThreadItem{ID: "cmd", Type: "commandExecution", Command: "bash -lc 'echo one; false'", AggregatedOutput: new("one\n"), ExitCode: new(1)}
			entry := sessionpresentation.ActivityEntry{Seq: 1, Text: "Run `echo one`\n\nRun `false`", Native: &sessionpresentation.NativeItem{Thread: "main", Turn: "t", Item: item.ID, Command: item.Command}}
			first, second := u.activity.Outputs(u.attached.session.Scope()).New(), u.activity.Outputs(u.attached.session.Scope()).New()
			if mode == "truncated" {
				first.Write(strings.Repeat("line\n", activitymodel.OutputBytes/5))
			} else if mode == "long-line" {
				first.Write(strings.Repeat("x", activitymodel.OutputLineBytes+1))
			} else {
				first.Write("one\n")
			}
			first.Finish(nil, new(0))
			second.Finish(nil, new(1))
			view := execTrackView{complete: true, output: true, code: 1, segments: []sessionpresentation.ActivitySegment{{Output: first}, {Output: second, Exit: 1}}}
			switch mode {
			case "lossy":
				view.output = false
			case "released":
				first.Release()
			case "incomplete":
				view.complete = false
			case "wrong-exit":
				view.code = 0
			}
			if mode != "missing" {
				retainCommandSegments(u.ctx, u.retained, u.segments, &u.attached, &u.commandSegmentWrites, &u.commandSegmentPending, u.issues, &u.dirty, entry, item.Observation(), view)
				awaitCommandSegments(t, u)
			}
			if mode == "storage-failure" {
				notices := u.issues.Notices.Pending()
				if len(notices) != 1 || !strings.Contains(notices[0], "limit is 1 bytes") || !u.dirty {
					t.Fatalf("storage failure omitted its actual cause: %v", notices)
				}
			}
			if mode == "corrupt" {
				path := filepath.Join(store.directory, reports.Name(workspace, reports.ID("t", item.ID)))
				if err := os.WriteFile(path, []byte("broken"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			u.attached.session.CompleteCommand(entry.Native, item.Observation(), u.activity.Outputs(u.attached.session.Scope()))
			restoreCommandSegments(u.ctx, u.retained, u.segments, &u.attached, u.activity, u.restoredSegments, &entry, item, workspace)
			u.attached.source.Entries = []sessionpresentation.ActivityRecord{{Source: entry}}
			pages := activityui.Disclosure(nil).Blocks(sessionpresentation.CommandPresentation(entry).DetailValues())
			if len(pages) != 1 || pages[0].Label != "combined output" || u.attached.source.RetainedOutput(pages[0].Source, pages[0].Output).View().Exit != 1 || strings.Join(u.attached.source.RetainedOutput(pages[0].Source, pages[0].Output).View().Lines, "\n") != "one" {
				t.Fatalf("missing evidence fabricated output: %+v", pages)
			}
			wantStates := mode == "lossy" || mode == "released" || mode == "truncated" || mode == "long-line"
			if (len(entry.Native.Segments) == 2) != wantStates {
				t.Fatalf("wrong retained states: %+v", entry.Native.Segments)
			}
			if wantStates && entry.Native.Segments[1].Exit != 1 {
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
		view := newActivityAdapter()
		view.Entries = []sessionpresentation.ActivityRecord{{Source: sessionpresentation.ActivityEntry{Seq: 1, Text: "Read `a.go`\n\nRead `b.go`", Native: &sessionpresentation.NativeItem{Command: command}}}}
		pages := activityui.Disclosure(nil).Blocks(sessionpresentation.CommandPresentation(view.Entries[0].Source).DetailValues())
		if len(pages) != 1 || pages[0].Code != sessionpresentation.DisplayCommand(command) || view.Entries[0].Source.Native.Command != command {
			t.Fatalf("combined display changed execution or leaked wrapper: %+v", pages)
		}
		ui := &terminalUI{Attachment: &terminalsession.Attachment{}}
		ui.openBlocks(view.bind(ui), pages)
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
	setTestAppServerIntegration(u, testProxyWithStore(store))
	listing := "amber1..amber5 +343 -658\namber6 +158 -81\n"
	item := binding.ThreadItem{ID: "cmd", Type: "commandExecution", Command: "bash -lc 'echo before; mchanges --list'", ExitCode: new(0), AggregatedOutput: new("before\n" + listing)}
	entry := sessionpresentation.ActivityEntry{Native: &sessionpresentation.NativeItem{Thread: "main", Turn: "t", Item: item.ID}}
	first, second := u.activity.Outputs(u.attached.session.Scope()).New(), u.activity.Outputs(u.attached.session.Scope()).New()
	first.Finish(new("before"), new(0))
	second.Finish(&listing, new(0))
	retainCommandSegments(u.ctx, u.retained, u.segments, &u.attached, &u.commandSegmentWrites, &u.commandSegmentPending, u.issues, &u.dirty, entry, item.Observation(), execTrackView{complete: true, output: true, segments: []sessionpresentation.ActivitySegment{{Output: first}, {Output: second}}})
	awaitCommandSegments(t, u)
	u.restoreHistory([]binding.HistoryTurn{{ID: "t", Status: "completed", Items: []binding.ThreadItem{item}}})
	// History starts collapsed; inspect the expanded retained command.
	var rows []string
	for _, restored := range u.attached.source.Entries {
		for _, block := range commandSegmentBlocks(sessionpresentation.CommandPresentation(restored.Source)) {
			block.Collapsed = false
			rows = append(rows, u.shell.Transcript.Painter.Block(block, 90)...)
		}
	}
	frame := ansi.Strip(strings.Join(rows, "\n"))
	if !strings.Contains(frame, "amber1..amber5  +343 -658") || strings.Contains(frame, "┆ amber") {
		t.Fatalf("restored listing lost rich change rows:\n%s", frame)
	}
}
