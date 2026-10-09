package router

import (
	json "encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
	"github.com/yusing/mekugi/internal/uisnapshot"
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
	u.view.conversation = true
	start := hookTestRun(u.session.cwd, "running")
	start["completedAt"], start["durationMs"], start["entries"] = nil, nil, nil
	appServerTestNotify(t, u, "hook/started", map[string]any{"threadId": "main", "run": start})
	if len(u.view.entries) != 1 || !u.view.entries[0].blocks[0].Running {
		t.Fatal("hook start did not create a running Run block")
	}
	appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "main", "turn": appServerTurn{ID: "t", Status: "completed"}})
	if !u.view.entries[0].blocks[0].Running {
		t.Fatal("turn completion ended an async hook")
	}
	appServerTestNotify(t, u, "hook/completed", map[string]any{"threadId": "main", "turnId": "later", "run": hookTestRun(u.session.cwd, "completed")})
	// Duplicate completion and a late start cannot add a row or revive work.
	appServerTestNotify(t, u, "hook/completed", map[string]any{"threadId": "main", "run": hookTestRun(u.session.cwd, "completed")})
	appServerTestNotify(t, u, "hook/started", map[string]any{"threadId": "main", "run": start})
	if len(u.view.entries) != 1 {
		t.Fatalf("hook notifications duplicated rows: %d", len(u.view.entries))
	}
	b := u.view.entries[0].blocks[0]
	if b.Running || b.Output.View().Exited || b.Duration != 120*time.Millisecond || !b.Started.Equal(time.Unix(1000, 0)) {
		t.Fatalf("hook completion borrowed a shell exit or lost host timing: %+v", b)
	}
	u.view.expansion = 1 // Default presentation omits hook runs.
	rows := ansi.Strip(strings.Join(u.view.renderConversation(80).lines, "\n"))
	if !strings.Contains(rows, "Hook Ran") || !strings.Contains(rows, "hooks.toml") || !strings.Contains(rows, "feedback: policy checked") {
		t.Fatalf("Main did not render hook source and output: %s", rows)
	}
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "later", "item": appServerItem{ID: "next", Type: "commandExecution", Command: "true", ExitCode: new(0)}})
	settleActivity(u.now().Add(time.Second), u.view, u.agents)
	if !u.view.entries[0].blocks[0].Collapsed {
		t.Fatal("successful hook output did not use Run collapse")
	}
}

func TestAppServerHookOutcomesInChildFeed(t *testing.T) {
	for _, status := range []string{"failed", "blocked", "stopped", "completed"} {
		t.Run(status, func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			waitTargetTestStore(t, u, waitTargetTestOpenStore(t))
			u.session.registerThread(appServerThreadInfo{ID: "child", AgentNickname: "worker"})
			u.session.metadata["child"] = ""
			run := hookTestRun(u.session.cwd, status)
			run["entries"] = []map[string]any{{"kind": "error", "text": "cannot execute\npermission denied\x1b]0;unsafe\a"}}
			// Completion alone must be visible, independently of Main's same run ID.
			appServerTestNotify(t, u, "hook/completed", map[string]any{"threadId": "main", "run": hookTestRun(u.session.cwd, "completed")})
			appServerTestNotify(t, u, "hook/completed", map[string]any{"threadId": "child", "run": run})
			b := u.agents.entries[len(u.agents.entries)-1].blocks[0]
			if b.Running || b.Collapsible() || b.ExitCode != 0 || b.Output.View().Exited || !b.Hook.HasError {
				t.Fatalf("hook outcome was flattened to a command exit: %+v", b)
			}
			page := u.agents.painter.DialogPage(b, 60)
			if !strings.Contains(page.Text, "error: cannot execute") || strings.Contains(page.Text, "unsafe") || strings.Contains(ansi.Strip(page.Detail), "exit") {
				t.Fatalf("hook detail lost errors or fabricated an exit: %+v", page)
			}
			if len(u.view.entries) != 1 {
				t.Fatal("child hook leaked into Main")
			}
		})
	}
}

func TestAppServerHookRetentionFailureKeepsLiveOutput(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	store := waitTargetTestOpenStore(t)
	waitTargetTestStore(t, u, store)
	if err := os.Mkdir(filepath.Join(store.directory, hookObservationsName(u.session.cwd, "main")), 0700); err != nil {
		t.Fatal(err)
	}
	appServerTestNotify(t, u, "hook/completed", map[string]any{"threadId": "main", "run": hookTestRun(u.session.cwd, "failed")})
	if len(u.view.entries) != 1 || !strings.Contains(u.notice, "could not be retained") {
		t.Fatal("retention failure hid live output or lacked a notice")
	}
}

func TestAppServerHookOpenDialogTracksCompletion(t *testing.T) {
	for _, status := range []string{"completed", "failed"} {
		t.Run(status, func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			waitTargetTestStore(t, u, waitTargetTestOpenStore(t))
			start := hookTestRun(u.session.cwd, "running")
			start["completedAt"], start["durationMs"], start["entries"] = nil, nil, nil
			appServerTestNotify(t, u, "hook/started", map[string]any{"threadId": "main", "run": start})
			if !u.shell.openEntry(u.view, u.view.entries[0].Seq) {
				t.Fatal("hook dialog did not open")
			}
			d := u.shell.output
			d.layout(80)
			d.query, d.draft, d.follow = "policy", "policy", false
			appServerTestNotify(t, u, "hook/completed", map[string]any{"threadId": "main", "run": hookTestRun(u.session.cwd, status)})
			d.layout(80)
			want := "Hook Ran"
			if status == "failed" {
				want = "Hook Failed"
			}
			if !strings.Contains(d.laid.Text, "feedback: policy checked") || !strings.Contains(ansi.Strip(d.laid.Title), want) || d.laid.Live {
				t.Fatalf("open dialog retained the start snapshot: %+v", d.laid)
			}
			if d.query != "policy" || d.draft != "policy" || d.follow {
				t.Fatal("hook completion reset dialog navigation")
			}
		})
	}
}

func TestAppServerHookPagedChildWithoutTurns(t *testing.T) {
	u, _ := newPagedHistoryUI(t)
	h := u.historyLoading
	observation := retainedHookObservation{Run: appServerHookRun{ID: "session-start-hook", EventName: "sessionStart", Status: "completed"},
		At: time.Unix(100, 0), Completed: true, Output: "feedback: session ready"}
	entry := u.session.hookEntry("child", observation, false)
	h.placements = append(h.placements, &restoredPlacement{entry: entry, at: observation.At})
	restoreContentReply(t, u, 3, map[string]any{"data": []any{}})
	if len(u.agents.entries) != 1 || u.agents.entries[0].Kind != "hook" || len(h.placements) != 0 || u.historyTarget() != nil {
		t.Fatalf("empty paginated history lost a hook or left false older history: entries=%d placements=%d", len(u.agents.entries), len(h.placements))
	}
}

func TestHookObservationRestartResumeAndReplay(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	directory, err := defaultMekugiReplayDirectory()
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
	appServerTestNotify(t, u, "hook/completed", map[string]any{"threadId": "main", "run": hookTestRun(u.session.cwd, "failed")})
	clock = clock.Add(time.Second)
	start := hookTestRun(u.session.cwd, "running")
	start["id"], start["entries"] = "unfinished", nil
	appServerTestNotify(t, u, "hook/started", map[string]any{"threadId": "main", "run": start})
	// A fresh store and UI have no live parent or process handles.
	fresh := newAppServerSessionTestUI(t, u.session.cwd)
	reopened, err := openMekugiReplayStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	waitTargetTestStore(t, fresh, reopened)
	root := appServerThreadInfo{ID: "main", Cwd: u.session.cwd}
	fresh.restoring = &appServerActivityRestore{root: root}
	fresh.readRestoredRollouts()
	fresh.restoreMainHistory(nil, fresh.restoring.main, nil)
	if len(fresh.view.entries) != 2 {
		t.Fatalf("resume lost hooks outside turns: %d", len(fresh.view.entries))
	}
	for _, entry := range fresh.view.entries {
		b := entry.blocks[0]
		if b.Running || !b.Output.View().Done {
			t.Fatal("resume revived a hook")
		}
	}
	if activityui.RowVerb(fresh.view.entries[1].blocks[0]) != "Hook Run" || !strings.Contains(fresh.view.entries[1].Text, "outcome unknown") {
		t.Fatal("unfinished restored hook claims completion")
	}
	foreign, err := reopened.readHookObservations("/other", "main")
	if err != nil || len(foreign.Runs) != 0 {
		t.Fatal("workspace scope leaked hook observations")
	}
	rollout := replayTestWrite(t, t.TempDir(), "rollout-main.jsonl", replayTestMeta("main"))
	source, err := readSessionUIReplay(t.Context(), rollout, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	playback := newUIReplayPlayback(t.Context(), source, 1)
	t.Cleanup(playback.close)
	if err := playback.advance(playback.until); err != nil {
		t.Fatal(err)
	}
	if len(playback.ui.view.entries) != 2 || playback.ui.session.waitStore != nil || playback.ui.view.entries[1].blocks[0].Running {
		t.Fatal("offline replay lost hooks or acquired live resources")
	}
	fresh.clock = u.clock
	end := hookTestRun(u.session.cwd, "completed")
	end["id"] = "unfinished"
	appServerTestNotify(t, fresh, "hook/completed", map[string]any{"threadId": "main", "run": end})
	if len(fresh.view.entries) != 2 || activityui.RowVerb(fresh.view.entries[1].blocks[0]) != "Hook Ran" {
		t.Fatal("a fresh host completion could not settle a restored unknown hook")
	}
	// Corrupt optional hook history is disclosed without losing the host replay.
	if err := os.WriteFile(filepath.Join(directory, hookObservationsName(u.session.cwd, "main")), []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	rollout = replayTestWrite(t, t.TempDir(), "rollout-main.jsonl", replayTestMeta("main"),
		replayTestRecord("event_msg", replayTestEpoch, map[string]any{"type": "task_started", "turn_id": "t"}),
		replayTestRecord("event_msg", replayTestEpoch+3000, map[string]any{"type": "task_complete", "turn_id": "t"}))
	source, err = readSessionUIReplay(t.Context(), rollout, "", 1)
	if err != nil || len(source.HookUnavailable) != 1 || len(source.Events) != 2 {
		t.Fatalf("invalid hook history blocked host replay or hid the gap: %v, %+v", err, source)
	}
}

func TestUISnapshotNativeHookVisibility(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	waitTargetTestStore(t, u, waitTargetTestOpenStore(t))
	u.view.conversation = true
	global := filepath.Join(u.session.cwd, "hooks.json")
	run := func(id, event, source, status, message string, entries ...map[string]any) {
		appServerTestNotify(t, u, "hook/completed", map[string]any{"threadId": "main", "run": map[string]any{
			"id": id, "eventName": event, "handlerType": "command", "executionMode": "sync", "sourcePath": source,
			"status": status, "statusMessage": message, "startedAt": 1000, "completedAt": 1001, "durationMs": 5, "entries": entries}})
	}
	run("guard", "preToolUse", builtinHookSource, "completed", "Applying VCS guard")
	run("context", "sessionStart", global, "completed", "", map[string]any{"kind": "context", "text": "<skills>"})
	run("rejected", "preToolUse", global, "blocked", "Checking generated files", map[string]any{"kind": "feedback", "text": "generated file"})
	rows := ansi.Strip(strings.Join(u.view.renderConversation(80).lines, "\n"))
	frames := []string{"default", rows}
	for expansion := uint8(1); expansion <= 2; expansion++ {
		u.view.expansion = expansion
		rows = ansi.Strip(strings.Join(u.view.renderConversation(80).lines, "\n"))
		frames = append(frames, fmt.Sprintf("expansion %d", expansion), rows)
	}
	uisnapshot.Assert(t, "testdata/snapshots/native-hook-visibility.txt", strings.Join(frames, "\n")+"\n")
}

func TestHookObservationMovesReleasedStatusLine(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	store := waitTargetTestOpenStore(t)
	waitTargetTestStore(t, u, store)
	// Observations retained before status messages were kept with the run.
	released := retainedHookObservations{Version: 1, Workspace: u.session.cwd, Thread: "main", Runs: []retainedHookObservation{{
		Run: appServerHookRun{ID: "generated", EventName: "preToolUse", HandlerType: "command", SourcePath: filepath.Join(u.session.cwd, "hooks.json"), Status: "completed"},
		At:  time.Unix(1000, 0), Output: "status: Checking for generated Go files\nfeedback: none found", Completed: true}}}
	data, err := json.Marshal(released)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.directory, hookObservationsName(u.session.cwd, "main")), data, 0600); err != nil {
		t.Fatal(err)
	}
	r, err := store.readHookObservations(u.session.cwd, "main")
	if err != nil {
		t.Fatal(err)
	}
	entry := u.session.hookEntry("main", r.Runs[0], false)
	if !strings.Contains(entry.Text, "Checking for generated Go files") || r.Runs[0].Output != "feedback: none found" {
		t.Fatalf("a released status line stayed output: %q, %q", entry.Text, r.Runs[0].Output)
	}
}
