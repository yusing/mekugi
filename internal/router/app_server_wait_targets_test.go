package router

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

func waitTargetTestAgent(t *testing.T, u *appServerUI, id, path string, running bool) {
	t.Helper()
	appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": id, "source": map[string]any{"subAgent": map[string]any{"thread_spawn": map[string]any{"agent_path": path}}}}})
	if running {
		appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": id, "turn": map[string]any{"id": "work"}})
	}
}

func waitTargetTestEvent(t *testing.T, u *appServerUI, method, caller, turn, id string) {
	t.Helper()
	status := "inProgress"
	if method == "item/completed" {
		status = "completed"
	}
	appServerTestNotify(t, u, method, map[string]any{"threadId": caller, "turnId": turn, "item": appServerItem{ID: id, Type: "collabAgentToolCall", Tool: "wait", Status: status}})
}

func waitTargetTestStore(t *testing.T, u *appServerUI, store *mekugiReplayStore) {
	t.Helper()
	if err := u.openWaitStore(store); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if u.waitRelease != nil {
			u.waitRelease()
			u.waitRelease = nil
		}
	})
}

func waitTargetTestOpenStore(t *testing.T) *mekugiReplayStore {
	t.Helper()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestAppServerWaitTargetsSnapshotAndRender(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	waitTargetTestStore(t, u, waitTargetTestOpenStore(t))
	waitTargetTestAgent(t, u, "a", "/root/alpha", true)
	waitTargetTestAgent(t, u, "b", "/root/beta", false)
	waitTargetTestAgent(t, u, "nested", "/root/alpha/nested", true)
	waitTargetTestEvent(t, u, "item/started", "main", "t", "w")
	if u.view.entries[0].native.wait == nil {
		t.Fatal("live wait lacks structured roster status")
	}
	want := "Waiting for agent · alpha, alpha/nested"
	if got := u.view.entries[0].Text; got != want {
		t.Fatalf("start = %q, want %q", got, want)
	}
	if rendered := u.view.renderFeed(120, 30).lines; len(rendered) != 0 {
		t.Fatalf("wait leaked into transcript: %q", rendered)
	}
	rendered, _ := u.agents.current(activityPaneAgent{Name: "/root"}, time.Now())
	if !strings.Contains(rendered, activityui.DimColor("/root/alpha")+"alpha") || !strings.Contains(rendered, activityui.DimColor("/root/alpha/nested")+"alpha/nested") {
		t.Fatalf("missing muted targets: %q", rendered)
	}
	frame := ansi.Strip(rendered)
	if !strings.Contains(frame, "alpha/nested") {
		t.Fatalf("target absent from frame: %q", frame)
	}
	appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "a", "turn": map[string]any{"id": "work", "status": "completed"}})
	waitTargetTestAgent(t, u, "new", "/root/new", true)
	waitTargetTestEvent(t, u, "item/completed", "main", "t", "w")
	if len(u.view.entries) != 1 || u.view.entries[0].Text != "Finished waiting · alpha, alpha/nested" {
		t.Fatalf("completion changed start targets: %+v", u.view.entries)
	}
}

func TestAppServerWaitTargetsCallerTurnAndItemIsolation(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	waitTargetTestStore(t, u, waitTargetTestOpenStore(t))
	waitTargetTestAgent(t, u, "a", "/root/alpha", true)
	waitTargetTestAgent(t, u, "nested", "/root/alpha/nested", true)
	waitTargetTestAgent(t, u, "idle", "/root/alpha/idle", false)
	waitTargetTestAgent(t, u, "sibling", "/root/alphabeta", true)
	item := appServerItem{Type: "collabAgentToolCall", Tool: "wait"}
	root := u.waitItem(item, "main", "t", "w", true)
	child := u.waitItem(item, "a", "t", "w", true)
	if !slices.Equal(child.ReceiverThreadIDs, []string{"nested"}) {
		t.Fatalf("child borrowed self, idle, or sibling targets: %+v", child)
	}
	u.session.agent("/root/alpha/nested").Responding = false
	waitTargetTestAgent(t, u, "later", "/root/alpha/later", true)
	for _, key := range [][2]string{{"next", "w"}, {"t", "other"}} {
		got := u.waitItem(item, "a", key[0], key[1], true)
		if !slices.Equal(got.ReceiverThreadIDs, []string{"later"}) {
			t.Fatalf("new key reused old targets: %+v", got)
		}
	}
	if got := u.waitItem(item, "a", "t", "w", false); !slices.Equal(got.ReceiverThreadIDs, child.ReceiverThreadIDs) {
		t.Fatalf("child snapshot overwritten: %+v", got)
	}
	if got := u.waitItem(item, "main", "t", "w", false); !slices.Equal(got.ReceiverThreadIDs, root.ReceiverThreadIDs) {
		t.Fatalf("caller snapshots collided: %+v", got)
	}
}

func TestAppServerWaitTargetsExplicitStatusMapping(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	waitTargetTestStore(t, u, waitTargetTestOpenStore(t))
	waitTargetTestAgent(t, u, "a", "/root/alpha", false)
	waitTargetTestAgent(t, u, "other", "/root/other", true)
	item := appServerItem{Type: "collabAgentToolCall", Tool: "wait", ReceiverThreadIDs: []string{"a", "unregistered"}}
	u.waitItem(item, "main", "t", "w", true)
	item.AgentsStates = map[string]appServerAgentState{"a": {Status: "running"}, "unregistered": {Status: "completed"}}
	got := u.waitItem(item, "main", "t", "w", false)
	text, _, _ := appServerProgress(got, "item/completed")
	if text != "Finished waiting · alpha: Still running, unregistered: completed" {
		t.Fatalf("explicit targets/statuses changed: %q", text)
	}
}

func TestAppServerWaitTargetsDurableReplay(t *testing.T) {
	workspace, store := t.TempDir(), waitTargetTestOpenStore(t)
	first := newAppServerSessionTestUI(t, workspace)
	waitTargetTestStore(t, first, store)
	waitTargetTestAgent(t, first, "a", "/root/alpha", true)
	waitTargetTestAgent(t, first, "nested", "/root/alpha/nested", true)
	for _, caller := range []string{"main", "a"} {
		waitTargetTestEvent(t, first, "item/started", caller, "t", "w")
	}
	history := []appServerHistoryTurn{{ID: "t", Status: "completed", Items: []appServerItem{{ID: "w", Type: "collabAgentToolCall", Tool: "wait", Status: "completed"}}}}
	restored := newAppServerSessionTestUI(t, workspace)
	waitTargetTestStore(t, restored, store)
	restored.restoreHistory(history)
	if restored.view.entries[0].native.wait == nil {
		t.Fatal("restored wait lacks structured roster status")
	}
	if got := restored.view.entries[0].Text; got != "Finished waiting · alpha, alpha/nested" {
		t.Fatalf("main replay lost targets: %q", got)
	}
	if rendered := restored.view.renderFeed(120, 30).lines; len(rendered) != 0 {
		t.Fatalf("restored wait leaked into transcript: %q", rendered)
	}
	rendered, _ := restored.agents.current(activityPaneAgent{Name: "/root"}, time.Now())
	if !strings.Contains(rendered, activityui.DimColor("/root/alpha")+"alpha") {
		t.Fatalf("restored wait lost color: %q", rendered)
	}
	restored.session.registerThread(appServerThreadInfo{ID: "a", AgentNickname: "alpha"})
	restored.restoreActivityThread(appServerThreadInfo{ID: "a", Turns: history})
	if got := restored.agents.entries[0].Text; got != "Finished waiting · alpha/nested" {
		t.Fatalf("child replay lost targets: %q", got)
	}
	if restored.turn != "" {
		t.Fatal("replay revived main turn")
	}
	for _, agent := range restored.session.agents {
		if agent.Responding {
			t.Fatalf("replay revived agent: %+v", agent)
		}
	}
	for _, dir := range []string{t.TempDir(), workspace} {
		fresh := newAppServerSessionTestUI(t, dir)
		freshStore := store
		if dir == workspace {
			freshStore = waitTargetTestOpenStore(t)
		}
		waitTargetTestStore(t, fresh, freshStore)
		waitTargetTestAgent(t, fresh, "today", "/root/today", true)
		fresh.restoreHistory(history)
		if got := fresh.view.entries[0].Text; got != "Finished waiting" {
			t.Fatalf("missing scoped evidence inferred targets: %q", got)
		}
	}
}

func TestAppServerWaitTargetsStorageFailureKeepsLiveDisplay(t *testing.T) {
	for _, mode := range []string{"malformed", "write failure"} {
		t.Run(mode, func(t *testing.T) {
			workspace := t.TempDir()
			u := newAppServerSessionTestUI(t, workspace)
			store := waitTargetTestOpenStore(t)
			waitTargetTestStore(t, u, store)
			if mode == "malformed" {
				waitTargetTestAgent(t, u, "a", "/root/alpha", true)
				waitTargetTestEvent(t, u, "item/started", "main", "t", "w")
				if err := os.WriteFile(u.session.waitTargetsPath([3]string{"main", "t", "w"}), []byte("not json"), 0600); err != nil {
					t.Fatal(err)
				}
				u = newAppServerSessionTestUI(t, workspace)
				waitTargetTestStore(t, u, store)
				waitTargetTestAgent(t, u, "a", "/root/alpha", true)
			} else {
				path := u.session.waitTargetsPath([3]string{"main", "t", "w"})
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
				waitTargetTestAgent(t, u, "a", "/root/alpha", true)
			}
			waitTargetTestEvent(t, u, "item/started", "main", "t", "w")
			if got := u.view.entries[0].Text; got != "Waiting for agent · alpha" {
				t.Fatalf("storage blocked live start: %q", got)
			}
			if !strings.Contains(u.notice, "Wait targets could not") {
				t.Fatalf("storage failure missing notice: %q", u.notice)
			}
			waitTargetTestEvent(t, u, "item/completed", "main", "t", "w")
			if got := u.view.entries[0].Text; got != "Finished waiting · alpha" {
				t.Fatalf("storage blocked live completion: %q", got)
			}
		})
	}
}

func TestAppServerWaitTargetsManagedRetention(t *testing.T) {
	store := waitTargetTestOpenStore(t)
	create := func(thread, workspace string) (string, func()) {
		u := newAppServerSessionTestUI(t, workspace)
		u.thread = thread
		u.session.start(thread, workspace)
		if err := u.openWaitStore(store); err != nil {
			t.Fatal(err)
		}
		item := appServerItem{Type: "collabAgentToolCall", Tool: "wait", ReceiverThreadIDs: []string{"target"}}
		u.waitItem(item, thread, "turn", "wait", true)
		path := u.session.waitTargetsPath([3]string{thread, "turn", "wait"})
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("wait record was not retained: %v", err)
		}
		return path, u.waitRelease
	}
	oldPath, releaseOld := create("old", "/old")
	releaseOld()
	retentionTestAge(t, store, "old", 30*24*time.Hour)
	sharedPath, releaseSharedSource := create("shared-source", "/shared")
	releaseSharedSource()
	retentionTestAge(t, store, "shared-source", 30*24*time.Hour)
	sharedCtx, releaseSharedOwner := retentionTestSession(t, store, "shared-owner", 0)
	if err := store.locked(sharedCtx, func() error {
		return store.scoped(sharedCtx).retainFiles(filepath.Base(sharedPath))
	}); err != nil {
		t.Fatal(err)
	}
	releaseSharedOwner()
	activePath, releaseActive := create("active", "/active")
	t.Cleanup(releaseActive)
	retentionTestAge(t, store, "active", 30*24*time.Hour)
	current, _ := retentionTestSession(t, store, "current", 0)
	if err := store.cleanupSessions(current); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, path string
		want       bool
	}{{"inactive", oldPath, false}, {"shared", sharedPath, true}, {"active", activePath, true}} {
		_, err := os.Stat(tc.path)
		if (err == nil) != tc.want {
			t.Errorf("%s wait record: exists=%v want=%v err=%v", tc.name, err == nil, tc.want, err)
		}
	}
}
