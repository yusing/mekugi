package router

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/backends/codex/binding"

	json "encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/modules/sessionhistory/history"

	activitymodel "github.com/yusing/mekugi/modules/activity/feed"

	"github.com/charmbracelet/x/ansi"
	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func hookTestRun(workspace, status string) map[string]any {
	return map[string]any{"id": "hook", "eventName": "preToolUse", "handlerType": "command", "executionMode": "async",
		"sourcePath": filepath.Join(workspace, "hooks.toml"), "status": status, "startedAt": 1000,
		"completedAt": 1001, "durationMs": 120, "statusMessage": "Checking policy",
		"entries": []map[string]any{{"kind": "context", "text": "one\ntwo\nthree"}, {"kind": "feedback", "text": "policy checked"}}}
}

func TestAppServerHookLifecycleUsesRunPresentation(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	waitTargetTestStore(t, u, waitTargetTestOpenStore(t))
	u.view.Conversation = true
	u.testActivity(u.view).testAudience()
	start := hookTestRun(u.cwd, "running")
	start["completedAt"], start["durationMs"], start["entries"] = nil, nil, nil
	appServerTestNotify(t, u, "hook/started", map[string]any{"threadId": "main", "run": start})
	if len(u.source.Entries) != 1 || !u.testActivity(u.view).blocks(0)[0].Running {
		t.Fatal("hook start did not create a running Run block")
	}
	appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "main", "turn": binding.Turn{ID: "t", Status: "completed"}})
	if !u.testActivity(u.view).blocks(0)[0].Running {
		t.Fatal("turn completion ended an async hook")
	}
	appServerTestNotify(t, u, "hook/completed", map[string]any{"threadId": "main", "turnId": "later", "run": hookTestRun(u.cwd, "completed")})
	// Duplicate completion and a late start cannot add a row or revive work.
	appServerTestNotify(t, u, "hook/completed", map[string]any{"threadId": "main", "run": hookTestRun(u.cwd, "completed")})
	appServerTestNotify(t, u, "hook/started", map[string]any{"threadId": "main", "run": start})
	if len(u.source.Entries) != 1 {
		t.Fatalf("hook notifications duplicated rows: %d", len(u.source.Entries))
	}
	b := u.testActivity(u.view).blocks(0)[0]
	if b.Running || u.source.RetainedOutput(b.Source, b.Output).View().Exited || b.Duration != 120*time.Millisecond || !b.Started.Equal(time.Unix(1000, 0)) {
		t.Fatalf("hook completion borrowed a shell exit or lost host timing: %+v", b)
	}
	u.view.Expansion = 1 // Default presentation omits hook runs.
	rows := ansi.Strip(strings.Join(u.testActivity(u.view).testRenderConversation(80).Lines, "\n"))
	if !strings.Contains(rows, "Hook Ran") || !strings.Contains(rows, "hooks.toml") || !strings.Contains(rows, "feedback: policy checked") {
		t.Fatalf("Main did not render hook source and output: %s", rows)
	}
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "later", "item": binding.ThreadItem{ID: "next", Type: "commandExecution", Command: "true", ExitCode: new(0)}})
	settleActivity(u.now().Add(activityui.OutputDebounce), u.testActivity(u.view), u.testActivity(u.agents))
	if !u.testActivity(u.view).blocks(0)[0].Collapsed {
		t.Fatal("successful hook output did not use Run collapse")
	}
}

func TestAppServerHookOutcomesInChildFeed(t *testing.T) {
	for _, status := range []string{"failed", "blocked", "stopped", "completed"} {
		t.Run(status, func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			waitTargetTestStore(t, u, waitTargetTestOpenStore(t))
			u.registerThread(binding.ThreadInfo{ID: "child", AgentNickname: "worker"})
			_ = u.requests.ReadThreadMetadata("main", "child", true, appServerRequest{})
			run := hookTestRun(u.cwd, status)
			run["entries"] = []map[string]any{{"kind": "error", "text": "cannot execute\npermission denied\x1b]0;unsafe\a"}}
			// Completion alone must be visible, independently of Main's same run ID.
			appServerTestNotify(t, u, "hook/completed", map[string]any{"threadId": "main", "run": hookTestRun(u.cwd, "completed")})
			appServerTestNotify(t, u, "hook/completed", map[string]any{"threadId": "child", "run": run})
			b := u.testActivity(u.agents).blocks(len(u.childSource.Entries) - 1)[0]
			if b.Running || b.Collapsible() || b.ExitCode != 0 || u.childSource.RetainedOutput(b.Source, b.Output).View().Exited || !b.Hook.HasError {
				t.Fatalf("hook outcome was flattened to a command exit: %+v", b)
			}
			page := u.agents.Painter.DialogPage(snapshotOutput(b, u.childSource.RetainedOutput(b.Source, b.Output)), 60)
			if !strings.Contains(page.Text, "error: cannot execute") || strings.Contains(page.Text, "unsafe") || strings.Contains(ansi.Strip(page.Detail), "exit") {
				t.Fatalf("hook detail lost errors or fabricated an exit: %+v", page)
			}
			if len(u.source.Entries) != 1 {
				t.Fatal("child hook leaked into Main")
			}
		})
	}
}

func TestAppServerHookRetentionFailureKeepsLiveOutput(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	store := waitTargetTestOpenStore(t)
	waitTargetTestStore(t, u, store)
	if err := os.Mkdir(filepath.Join(store.directory, history.HookObservationsName(u.cwd, "main")), 0700); err != nil {
		t.Fatal(err)
	}
	appServerTestNotify(t, u, "hook/completed", map[string]any{"threadId": "main", "run": hookTestRun(u.cwd, "failed")})
	if len(u.source.Entries) != 1 || !strings.Contains(u.feedback.Text, "could not be retained") {
		t.Fatal("retention failure hid live output or lacked a notice")
	}
}

func TestAppServerHookOpenDialogTracksCompletion(t *testing.T) {
	for _, status := range []string{"completed", "failed"} {
		t.Run(status, func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			waitTargetTestStore(t, u, waitTargetTestOpenStore(t))
			start := hookTestRun(u.cwd, "running")
			start["completedAt"], start["durationMs"], start["entries"] = nil, nil, nil
			appServerTestNotify(t, u, "hook/started", map[string]any{"threadId": "main", "run": start})
			if !terminalTest(u).openEntry(u.view, u.source.Entries[0].Source.Seq) {
				t.Fatal("hook dialog did not open")
			}
			d := terminalTest(u).Dialog
			terminalTest(u).layoutOutput(80)
			for _, key := range []string{"g", "/", "policy"} {
				terminalTest(u).outputKey(key)
			}
			appServerTestNotify(t, u, "hook/completed", map[string]any{"threadId": "main", "run": hookTestRun(u.cwd, status)})
			terminalTest(u).layoutOutput(80)
			want := "Hook Ran"
			if status == "failed" {
				want = "Hook Failed"
			}
			if !strings.Contains(d.Document.Text, "feedback: policy checked") || !strings.Contains(ansi.Strip(d.Document.Title), want) || d.Document.Live {
				t.Fatalf("open dialog retained the start snapshot: %+v", d.Document)
			}
			if !strings.Contains(drawOutputDialog(terminalTest(u)), "/policy▏") {
				t.Fatal("hook completion reset dialog navigation")
			}
		})
	}
}

func TestAppServerHookPagedChildWithoutTurns(t *testing.T) {
	u, _ := newPagedHistoryUI(t)
	h := u.historyLoading()
	observation := history.HookObservation{Run: history.HookRun{ID: "session-start-hook", EventName: "sessionStart", Status: "completed"},
		At: time.Unix(100, 0), Completed: true, Output: "feedback: session ready"}
	entry := u.hookEntry("child", observation, false)
	h.placements = append(h.placements, &restoredPlacement{entry: entry, at: observation.At})
	restoreContentReply(t, u, 3, map[string]any{"data": []any{}})
	if len(u.childSource.Entries) != 1 || u.childSource.Entries[0].Source.Kind != "hook" || len(h.placements) != 0 || historyLoadIntent(u).Thread != "" {
		t.Fatalf("empty paginated history lost a hook or left false older history: entries=%d placements=%d", len(u.childSource.Entries), len(h.placements))
	}
}

func TestHookObservationRestartResumeAndReplay(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	directory, err := history.ReplayDirectory()
	if err != nil {
		t.Fatal(err)
	}
	store, err := openMekugiReplayStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	u := newAppServerSessionTestUI(t, "/workspace/replay")
	waitTargetTestStore(t, u, store)
	clock := time.UnixMilli(replayTestEpoch + 1000)
	u.clock = func() time.Time { return clock }
	appServerTestNotify(t, u, "hook/completed", map[string]any{"threadId": "main", "run": hookTestRun(u.cwd, "failed")})
	clock = clock.Add(time.Second)
	start := hookTestRun(u.cwd, "running")
	start["id"], start["entries"] = "unfinished", nil
	appServerTestNotify(t, u, "hook/started", map[string]any{"threadId": "main", "run": start})
	// A fresh store and UI have no live parent or process handles.
	fresh := newAppServerSessionTestUI(t, u.cwd)
	reopened, err := openMekugiReplayStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	waitTargetTestStore(t, fresh, reopened)
	root := binding.ThreadInfo{ID: "main", Cwd: u.cwd}
	fresh.restoring = &appServerActivityRestore{root: root, state: sessionpresentation.NewRestoration(root.ID)}
	fresh.readRestoredRollouts()
	fresh.restoreMainHistory(nil, fresh.restoring.main, nil)
	if len(fresh.source.Entries) != 2 {
		t.Fatalf("resume lost hooks outside turns: %d", len(fresh.source.Entries))
	}
	for _, entry := range fresh.source.Entries {
		b := fresh.view.Disclosure.Blocks(entry.Values)[0]
		if b.Running || !fresh.source.RetainedOutput(b.Source, b.Output).View().Done {
			t.Fatal("resume revived a hook")
		}
	}
	if activityui.RowVerb(fresh.testActivity(fresh.view).blocks(1)[0]) != "Hook Run" || !strings.Contains(fresh.source.Entries[1].Source.Text, "outcome unknown") {
		t.Fatal("unfinished restored hook claims completion")
	}
	foreign, err := reopened.publication.ReadHookObservations("/other", "main")
	if err != nil || len(foreign.Runs) != 0 {
		t.Fatal("workspace scope leaked hook observations")
	}
	rollout := replayTestWrite(t, t.TempDir(), "rollout-main.jsonl", replayTestMeta("main"))
	source, err := replayReadTest(t, new(transportcapture.Service), t.Context(), rollout, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	playback := newTestUIReplayPlayback(new(activitymodel.Service), t.Context(), source, 1)
	t.Cleanup(playback.close)
	if err := playback.advance(playback.Until); err != nil {
		t.Fatal(err)
	}
	if len(playback.ui.source.Entries) != 2 || playback.ui.retainedSession.Store != nil || playback.ui.testActivity(playback.ui.view).blocks(1)[0].Running {
		t.Fatal("offline replay lost hooks or acquired live resources")
	}
	fresh.clock = u.clock
	end := hookTestRun(u.cwd, "completed")
	end["id"] = "unfinished"
	appServerTestNotify(t, fresh, "hook/completed", map[string]any{"threadId": "main", "run": end})
	if len(fresh.source.Entries) != 2 || activityui.RowVerb(fresh.testActivity(fresh.view).blocks(1)[0]) != "Hook Ran" {
		t.Fatal("a fresh host completion could not settle a restored unknown hook")
	}
	// Corrupt optional hook history is disclosed without losing the host replay.
	if err := os.WriteFile(filepath.Join(directory, history.HookObservationsName(u.cwd, "main")), []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	rollout = replayTestWrite(t, t.TempDir(), "rollout-main.jsonl", replayTestMeta("main"),
		replayTestRecord("event_msg", replayTestEpoch, map[string]any{"type": "task_started", "turn_id": "t"}),
		replayTestRecord("event_msg", replayTestEpoch+3000, map[string]any{"type": "task_complete", "turn_id": "t"}))
	source, err = replayReadTest(t, new(transportcapture.Service), t.Context(), rollout, "", 1)
	if err != nil || len(source.HookUnavailable) != 1 || len(source.Events) != 2 {
		t.Fatalf("invalid hook history blocked host replay or hid the gap: %v, %+v", err, source)
	}
}

func TestUISnapshotNativeHookVisibility(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	waitTargetTestStore(t, u, waitTargetTestOpenStore(t))
	u.view.Conversation = true
	u.testActivity(u.view).testAudience()
	global := filepath.Join(u.cwd, "hooks.json")
	run := func(id, event, source, status, message string, entries ...map[string]any) {
		appServerTestNotify(t, u, "hook/completed", map[string]any{"threadId": "main", "run": map[string]any{
			"id": id, "eventName": event, "handlerType": "command", "executionMode": "sync", "sourcePath": source,
			"status": status, "statusMessage": message, "startedAt": 1000, "completedAt": 1001, "durationMs": 5, "entries": entries}})
	}
	run("guard", "preToolUse", builtinHookSource, "completed", "Applying shell guard")
	run("context", "sessionStart", global, "completed", "", map[string]any{"kind": "context", "text": "<skills>"})
	run("rejected", "preToolUse", global, "blocked", "Checking generated files", map[string]any{"kind": "feedback", "text": "generated file"})
	rows := ansi.Strip(strings.Join(u.testActivity(u.view).testRenderConversation(80).Lines, "\n"))
	frames := []string{"default", rows}
	for expansion := uint8(1); expansion <= 2; expansion++ {
		u.view.Expansion = expansion
		rows = ansi.Strip(strings.Join(u.testActivity(u.view).testRenderConversation(80).Lines, "\n"))
		frames = append(frames, fmt.Sprintf("expansion %d", expansion), rows)
	}
	uisnapshot.Assert(t, "testdata/snapshots/native-hook-visibility.txt", strings.Join(frames, "\n")+"\n")
	// Native guard rejection carries the parser's reason, not a command exit.
	appServerTestNotify(t, u, "hook/completed", map[string]any{"threadId": "main", "run": map[string]any{
		"id": "guard-rejected", "eventName": "preToolUse", "handlerType": "mcpTool", "executionMode": "sync",
		"sourcePath": builtinHookSource, "status": "blocked", "statusMessage": "Applying shell guard",
		"startedAt": 1000, "completedAt": 1001, "durationMs": 5,
		"entries": []map[string]any{{"kind": "feedback", "text": "mekugi: VCS guard instrumentation failed: VCS guard: cannot parse command: 1:10: reached EOF without closing quote `'`"}}}})
	uisnapshot.Assert(t, "testdata/snapshots/native-hook-guard-rejection.txt", strings.Join(u.testActivity(u.view).testRenderConversation(80).Lines, "\n")+"\n")
	if !terminalTest(u).openEntry(u.view, u.source.Entries[len(u.source.Entries)-1].Source.Seq) {
		t.Fatal("guard rejection dialog did not open")
	}
	uisnapshot.Assert(t, "testdata/snapshots/native-hook-guard-rejection-dialog.txt", drawOutputDialog(terminalTest(u))+"\n")
}

func TestHookObservationMovesReleasedStatusLine(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	store := waitTargetTestOpenStore(t)
	waitTargetTestStore(t, u, store)
	// Observations retained before status messages were kept with the run.
	released := history.HookObservations{Version: 1, Workspace: u.cwd, Thread: "main", Runs: []history.HookObservation{{
		Run: history.HookRun{ID: "generated", EventName: "preToolUse", HandlerType: "command", SourcePath: filepath.Join(u.cwd, "hooks.json"), Status: "completed"},
		At:  time.Unix(1000, 0), Output: "status: Checking for generated Go files\nfeedback: none found", Completed: true}}}
	data, err := json.Marshal(released)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.directory, history.HookObservationsName(u.cwd, "main")), data, 0600); err != nil {
		t.Fatal(err)
	}
	r, err := store.publication.ReadHookObservations(u.cwd, "main")
	if err != nil {
		t.Fatal(err)
	}
	entry := u.hookEntry("main", r.Runs[0], false)
	if !strings.Contains(entry.Text, "Checking for generated Go files") || r.Runs[0].Output != "feedback: none found" {
		t.Fatalf("a released status line stayed output: %q, %q", entry.Text, r.Runs[0].Output)
	}
}
