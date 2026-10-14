package router

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/gofrs/flock"
	"github.com/yusing/mekugi"
	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/ui/diffview"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func TestAppServerOrchestrateActivityScope(t *testing.T) {
	for _, replay := range []bool{false, true} {
		t.Run(fmt.Sprintf("replay=%v", replay), func(t *testing.T) {
			u, launch := orchestrateIdentityPendingTurn(t)
			orchestrateTestReply(t, u, launch, `{"turn":{"id":"batch-turn"}}`)
			for _, root := range []string{"main", "child"} {
				worker := root + "-worker"
				orchestrateTestMessage(t, u, fmt.Sprintf(`{"method":"thread/started","params":{"thread":{"id":%q,"source":{"subAgent":{"thread_spawn":{"parent_thread_id":%q,"agent_path":"/root/worker"}}}}}}`, worker, root))
				v := u.navigation.views[root]
				for _, thread := range []string{root, worker} {
					item := appServerItem{ID: thread, Type: "commandExecution", Command: "cat " + thread + ".go", Status: "completed"}
					if replay {
						turns := []appServerHistoryTurn{{ID: "recorded", Status: "completed", Items: []appServerItem{item}}}
						if thread == root {
							v.restoreMainHistory(turns, nil, nil)
						} else {
							info := v.session.threads[thread]
							info.Turns = turns
							v.restoreActivityThread(info)
						}
					} else {
						appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": thread, "turnId": "live", "item": item})
					}
				}
			}
			for _, root := range []string{"child", "main", "child"} {
				u.switchOrchestratedThread(root)
				v := u.viewedUI()
				feed := ansi.Strip(strings.Join(v.agents.renderFeed(90, 60).lines, "\n"))
				other := "main"
				if root == "main" {
					other = "child"
				}
				if !strings.Contains(feed, root+"-worker.go") || strings.Contains(feed, root+".go") || strings.Contains(feed, other+"-worker.go") {
					t.Fatalf("%s Activity crossed its native-child scope:\n%s", root, feed)
				}
				if !strings.Contains(ansi.Strip(strings.Join(v.view.renderFeed(90, 60).lines, "\n")), root+".go") {
					t.Fatal("root activity left its own transcript", root)
				}
			}
		})
	}
}

func TestAppServerOrchestrateSwitchPane(t *testing.T) {
	u, launch := orchestrateIdentityPendingTurn(t)
	orchestrateTestReply(t, u, launch, `{"turn":{"id":"batch-turn"}}`)
	v := u.navigation.views["child"]
	for _, view := range []*appServerUI{u, v} {
		view.modelsLoading = true
		view.agents.selected, view.agents.only = "/root/"+view.thread, true
	}
	for _, pane := range []int{1, 2, 4} {
		for _, focus := range []int{0, pane, 3} {
			u.shell.selectNativePane(pane)
			u.shell.focus = focus
			v.shell.selectNativePane(4)
			u.switchOrchestratedThread("child")
			if v.shell.focus != focus || v.shell.diffOpen != u.shell.diffOpen || v.shell.journalOpen != u.shell.journalOpen {
				t.Fatalf("pane %d focus %d changed on switch", pane, focus)
			}
			u.switchOrchestratedThread("main")
			for _, view := range []*appServerUI{u, v} {
				if !view.agents.only || view.agents.selected != "/root/"+view.thread {
					t.Fatal("switch changed native Activity filter", view.thread)
				}
			}
		}
	}
}

func TestUISnapshotOrchestrationTemporaryActivity(t *testing.T) {
	for _, scenario := range []string{"returned", "completed-away", "manual-activity"} {
		t.Run(scenario, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			u, launch := orchestrateIdentityPendingTurn(t)
			orchestrateTestReply(t, u, launch, `{"turn":{"id":"batch-turn"}}`)
			v := u.navigation.views["child"]
			now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
			for _, view := range []*appServerUI{u, v} {
				view.clock = func() time.Time { return now }
				view.view.clock, view.agents.clock = view.clock, view.clock
				view.view.painter.Theme, view.agents.painter.Theme = livediff.DarkTheme, livediff.DarkTheme
				view.status, view.model, view.turn = "Ready", "snapshot-model", ""
				view.modelsLoading = true
			}
			assertPane := func(view *appServerUI, journal, temporary bool, focus int) {
				t.Helper()
				if view.shell.journalOpen != journal || view.shell.autoActivity != temporary || view.shell.focus != focus {
					t.Fatalf("%s pane journal=%v temporary=%v focus=%d", view.thread, view.shell.journalOpen, view.shell.autoActivity, view.shell.focus)
				}
				p := new(nativePanePersistence)
				if err := p.open(view.shell, view.session.cwd, view.thread, false); err != nil {
					t.Fatal(err)
				}
				p.pending = true
				if err := p.save(view.shell, now, true); err != nil {
					t.Fatal(err)
				}
				state, err := p.read(view.session.cwd, view.thread)
				if err != nil || state.JournalOpen != (journal || temporary) || state.Focus != focus {
					t.Fatalf("saved preference=%+v err=%v", state, err)
				}
			}
			snapshot := func(view *appServerUI, name string) {
				t.Helper()
				view.shell.paintedRows = nil
				var frame bytes.Buffer
				if err := view.paint(&frame, 100, 28); err != nil {
					t.Fatal(err)
				}
				screen := vt.NewEmulator(100, 28)
				defer screen.Close()
				if _, err := screen.Write(frame.Bytes()); err != nil {
					t.Fatal(err)
				}
				uisnapshot.Assert(t, "testdata/snapshots/orchestration-temporary-activity-"+name+".txt", screen.String()+"\n")
			}
			u.switchOrchestratedThread("child")
			assertPane(v, true, false, 0)
			appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "worker", "parentThreadId": "child", "agentNickname": "worker"}})
			assertPane(v, false, true, 0)
			snapshot(v, "started")
			u.switchOrchestratedThread("main")
			assertPane(u, false, true, 0)
			if scenario == "manual-activity" {
				for _, key := range []byte{2, '3'} {
					if err := u.shell.key(key); err != nil {
						t.Fatal(err)
					}
				}
			}
			if scenario != "completed-away" {
				u.switchOrchestratedThread("child")
			}
			appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "worker", "turn": map[string]any{"id": "work", "status": "completed"}})
			if scenario == "completed-away" {
				assertPane(u, true, false, 0)
				snapshot(u, scenario)
				u.switchOrchestratedThread("child")
			}
			manual := scenario == "manual-activity"
			focus := 0
			if manual {
				focus = 2
			}
			assertPane(v, !manual, false, focus)
			snapshot(v, map[bool]string{false: "returned", true: "manual"}[manual])
		})
	}
}

func TestAppServerOrchestrateFreshDiffAdmission(t *testing.T) {
	replay, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	u, launch := orchestrateIdentityPendingTurnWithReplay(t, replay)
	orchestrateTestReply(t, u, launch, `{"turn":{"id":"batch-turn"}}`)
	auto, stop := newAutoLiveDiff(t.Context(), replay.directory)
	defer stop()
	auto.enable()
	u.proxy.autoLiveDiff, u.shell.auto = auto, auto
	second := *u.orchestrateThreads["child"]
	second.batch.TaskName = "second"
	copyLaunch := *second.batch.Launch
	copyLaunch.ThreadID = "child2"
	second.batch.Launch = &copyLaunch
	u.orchestrateThreads["child2"] = &second
	workspace := second.batch.Cwd
	liveDiffScopeCapture(t, replay, workspace, "child2", "saved-edit", filepath.Join(workspace, "file"), "before", "after")
	sub := auto.events.subscribe()
	auto.includeThread(workspace, "child2", true)
	for len(sub.events) > 0 {
		u.applyOrchestrationDiff(t.Context(), <-sub.events)
	}
	// An authenticated request can publish its scope before the launch callback
	// creates a controller. A repeated includeThread emits no replacement scope.
	raw := mustMarshalJSON(map[string]any{"thread": appServerThreadInfo{ID: "child2", Cwd: workspace}})
	if err := u.addOrchestratedView(&second, second.batch.Launch.ThreadID, raw, func() {}); err != nil {
		t.Fatal(err)
	}
	drainOrchestrateWork(t, u)
	v := u.navigation.views["child2"]
	if len(v.shell.diff.data.files()) != 1 || len(u.shell.diff.data.files()) != 0 {
		t.Fatal("fresh child lost prepublished Diff scope or crossed roots")
	}
}

func TestAppServerOrchestrateSavedTaskDiff(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	u, launch := orchestrateIdentityPendingTurnWithReplay(t, store)
	orchestrateTestReply(t, u, launch, `{"turn":{"id":"batch-turn"}}`)
	v := u.navigation.views["child"]
	workspace := v.session.cwd
	for i, parent := range []string{"", "/1"} {
		if _, err := u.proxy.journals.apply(v.ctx, store, workspace, "child", "", []journalMutation{{Op: "add", Under: parent, Kind: "task", Title: new("Work"), State: new("working")}}); err != nil {
			t.Fatal(err)
		}
		call := fmt.Sprintf("edit-%d", i)
		id, err := store.reserveChange(v.ctx, workspace, "child", call)
		if err != nil {
			t.Fatal(err)
		}
		history := mekugiHistory{ChangeID: id, CorrelationID: call, Caller: "/root", ExecutingThread: "child", ReviewFiles: []mekugi.ReviewFile{mekugi.RenderReviewFile(workspace+"/"+call, workspace+"/"+call, "before\n", "after\n")}}
		if err := store.put(v.ctx, workspace, map[string]mekugiHistory{call: history}); err != nil {
			t.Fatal(err)
		}
	}
	// Restore using durable evidence only. The pane's mounted names differ
	// from capture callers, while the owned task identities remain local.
	reader := &mekugiReplayStore{directory: store.directory}
	if err := u.proxy.journals.restoreNative(v.ctx, reader, v.journal); err != nil {
		t.Fatal(err)
	}
	if v.journal.presented().Author != "/root/batch" {
		t.Fatal("fixture did not mount the orchestration journal")
	}
	u.applyOrchestrationDiff(v.ctx, liveDiffEvent{Kind: "scope", Scope: &liveDiffScope{Workspaces: map[string]map[string]bool{workspace: {"child": true}}}})
	v.shell.diffOpen, v.shell.journalOpen = true, false
	for _, want := range []int{1, 2, 2} {
		if err := v.paint(&bytes.Buffer{}, 120, 35); err != nil {
			t.Fatal(err)
		}
		if got := len(v.shell.diff.navigation.Matches); got != want {
			t.Fatalf("scope %v shows %d files, want %d", v.shell.diff.taskScope, got, want)
		}
		v.shell.diff.cycleTaskScope()
	}
}

func TestAppServerOrchestrateNavigationLineage(t *testing.T) {
	replay, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	u, launch := orchestrateIdentityPendingTurnWithReplay(t, replay)
	u.shell.diff.store = replay
	w := u.client.Input.(*appServerTestInput)
	orchestrateTestReply(t, u, launch, `{"turn":{"id":"batch-turn"}}`)
	v := u.navigation.views["child"]
	u.modelsLoading, v.modelsLoading = true, true // The catalog is not part of switching history.
	u.shell.diff.navigation.Columns, v.shell.diff.navigation.Columns = 30, 30
	u.draft = "main draft"
	keys := make(chan byte, 8)
	for _, key := range []byte{2, ']', 'c', 'h', 'i', 'l', 'd'} {
		keys <- key
	}
	if err := u.drainKeys(keys); err != nil {
		t.Fatal(err)
	}
	if u.viewedUI() != v || u.draft != "main draft" || v.draft != "child" {
		t.Fatal("buffered keys targeted the old composer", u.draft, v.draft)
	}
	for _, key := range []byte{2, '['} {
		if err := v.shell.key(key); err != nil {
			t.Fatal(err)
		}
	}
	if u.viewedUI() != u || u.draft != "main draft" || v.draft != "child" {
		t.Fatal("reverse cycling lost drafts")
	}
	if u.shell.diff.navigation.Columns != 30 || v.shell.diff.navigation.Columns != 30 {
		t.Fatal("thread cycling resized a Diff navigator")
	}
	for _, key := range []byte{2, ']'} {
		if err := u.shell.key(key); err != nil {
			t.Fatal(err)
		}
	}
	// Substantive output and native children remain isolated even with identical
	// native paths, including a lifecycle notification before parent metadata.
	orchestrateTestMessage(t, u, `{"method":"turn/started","params":{"threadId":"child","turn":{"id":"batch-turn"}}}`)
	orchestrateTestMessage(t, u, `{"method":"turn/started","params":{"threadId":"native-worker","turn":{"id":"worker-turn"}}}`)
	read := btwTestRequest(t, w, "thread/read", "native-worker")
	orchestrateTestReply(t, u, read, `{"thread":{"id":"native-worker","source":{"subAgent":{"thread_spawn":{"parent_thread_id":"child","agent_path":"/root/worker","agent_role":"worker"}}}}}`)
	if v.session.paths["native-worker"] != "/root/worker" || !v.session.agent("/root/worker").Responding || u.turn != "" {
		t.Fatal("late metadata lost the batch lifecycle", v.session.paths)
	}
	orchestrateTestMessage(t, u, `{"method":"item/completed","params":{"threadId":"child","turnId":"batch-turn","item":{"id":"answer","type":"agentMessage","channel":"final_answer","text":"Batch output"}}}`)
	if !strings.Contains(v.view.entries[len(v.view.entries)-1].Text, "Batch output") {
		t.Fatal("batch output missed its own transcript")
	}
	for _, entry := range u.view.entries {
		if entry.Text == "Batch output" {
			t.Fatal("batch output reached Main")
		}
	}
	scope := liveDiffScope{Workspaces: map[string]map[string]bool{u.session.cwd: {"main": true}, v.session.cwd: {"child": true, "native-worker": true}}}
	u.applyOrchestrationDiff(t.Context(), liveDiffEvent{Kind: "scope", Scope: &scope})
	if !v.shell.diff.scope.Workspaces[v.session.cwd]["native-worker"] || u.shell.diff.scope.Workspaces[v.session.cwd]["child"] || v.shell.diff.scope.Workspaces[u.session.cwd]["main"] {
		t.Fatal("Diff scope crossed roots")
	}
	u.applyOrchestrationDiff(t.Context(), liveDiffEvent{Kind: "preview", Preview: &diffview.Preview{ID: "edit", Workspace: v.session.cwd, Thread: "native-worker", Caller: "/root/worker", Input: "batch edit"}})
	if v.shell.livePending["edit"].preview.Thread != "native-worker" || len(u.shell.livePending) != 0 || len(u.shell.liveDock.Order) != 0 {
		t.Fatal("preview crossed thread views")
	}
	// A real roster click switches controllers before later keys are dispatched.
	v.orchestrationRoster()
	if err := v.paint(&bytes.Buffer{}, 120, 40); err != nil {
		t.Fatal(err)
	}
	for _, hit := range v.agents.hits {
		if hit.agent == "/Orchestration/main" && !hit.skills {
			r := v.shell.layout.roster
			for _, suffix := range []string{"M", "m"} {
				if err := v.shell.mouse(fmt.Sprintf("\x1b[<0;%d;%d%s", r.x+hit.first, r.y+hit.row, suffix)); err != nil {
					t.Fatal(err)
				}
			}
			break
		}
	}
	if u.viewedUI() != u {
		t.Fatal("roster click only filtered instead of switching")
	}
	u.switchOrchestratedThread("child")
	// Enter uses the selected roster thread, not the native Activity filter.
	v.orchestrationRoster()
	v.agents.rosterSelected = "/Orchestration/main"
	v.shell.focus = 3
	if err := v.shell.key('\r'); err != nil {
		t.Fatal(err)
	}
	if u.viewedUI() != u || u.draft != "main draft" {
		t.Fatal("roster Enter did not restore Main")
	}
	// Background root work remains admitted while another thread is viewed.
	u.switchOrchestratedThread("child")
	orchestrateTestMessage(t, u, `{"method":"thread/settings/updated","params":{"threadId":"main","threadSettings":{"model":"main-model","effort":"high"}}}`)
	if u.model != "main-model" || v.model == "main-model" {
		t.Fatal("settings reached the viewed thread instead of their source")
	}
	if !u.orchestrateBusy() {
		t.Fatal("navigation abandoned a running batch")
	}
	if _, err := u.proxy.applyJournal(v.session.waitContext, v.session.cwd, v.thread, "", []journalMutation{{Op: "add", Kind: "task", Title: new("Batch journal task"), State: new("working")}}); err != nil {
		t.Fatal(err)
	}
	v.applyPendingJournal()
	var journalFrame bytes.Buffer
	if err := v.paint(&journalFrame, 120, 40); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(journalFrame.String(), "Batch journal task") {
		t.Fatal("first fresh journal publication did not reach the selected shell")
	}
	// A rejected ordinary composer submission returns to that same draft.
	v.shell.focus = 0
	v.draft = "steer batch"
	if err := v.shell.key('\r'); err != nil {
		t.Fatal(err)
	}
	steer := btwTestRequest(t, w, "turn/steer", "child")
	orchestrateTestMessage(t, u, string(mustMarshalJSON(map[string]any{"id": steer.ID, "error": map[string]any{"code": -1, "message": "rejected"}})))
	if v.draft != "steer batch" || u.draft != "main draft" {
		t.Fatal("rejected input restored to the wrong thread")
	}
}

func TestUISnapshotOrchestrationNavigation(t *testing.T) {
	u, launch := orchestrateIdentityPendingTurn(t)
	orchestrateTestReply(t, u, launch, `{"turn":{"id":"batch-turn"}}`)
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	v := u.navigation.views["child"]
	u.orchestrateThreads["child"].batch.Branch = "mekugi/" + strings.Repeat("0123456789", 8) + "/batch"
	for _, view := range []*appServerUI{u, v} {
		view.clock = func() time.Time { return now }
		view.view.clock, view.agents.clock = view.clock, view.clock
		view.view.painter.Theme, view.agents.painter.Theme = livediff.DarkTheme, livediff.DarkTheme
		view.status, view.model = "Ready", "snapshot-model"
		view.turn, view.awaitingTurn = "", false
		view.modelsLoading = true
		view.shell.focus = 3
		view.agents.apply(activityPaneEvent{Kind: "agents", Agents: view.session.agents})
	}
	for _, thread := range []string{"main", "child"} {
		orchestrateTestMessage(t, u, fmt.Sprintf(`{"method":"item/completed","params":{"threadId":%q,"turnId":"turn","item":{"id":"read","type":"commandExecution","command":"cat %s.go","status":"completed","exitCode":0}}}`, thread, thread))
	}
	for _, view := range []*appServerUI{u, v} {
		view.shell.journalOpen = false
	}
	u.draft, v.draft = "Review the batch", "Complete the batch"
	for _, root := range []string{"main", "child"} {
		orchestrateTestMessage(t, u, fmt.Sprintf(`{"method":"thread/started","params":{"thread":{"id":%q,"parentThreadId":%q,"agentNickname":%q,"agentRole":%q}}}`, root+"-worker", root, map[string]string{"main": "main_worker", "child": "batch_worker"}[root], map[string]string{"main": "worker", "child": "review-correctness"}[root]))
		orchestrateTestMessage(t, u, fmt.Sprintf(`{"method":"item/completed","params":{"threadId":%q,"turnId":"turn","item":{"id":"read-worker","type":"commandExecution","command":"cat %s-worker.go","status":"completed","exitCode":0}}}`, root+"-worker", root))
	}
	for _, test := range []struct {
		name, thread string
		collapsed    bool
		width        int
	}{{"main", "main", false, 100}, {"batch", "child", false, 100}, {"returned", "main", false, 100}, {"collapsed", "main", true, 100}, {"narrow", "child", false, 45}} {
		selected := "/Orchestration/main"
		if test.thread == "child" {
			selected = "/Orchestration/batch"
		}
		u.viewedUI().agents.rosterSelected = selected
		u.switchOrchestratedThread(test.thread)
		if test.name == "returned" {
			test.name = "main" // Returning must restore the same selected-row style.
		}
		view := u.viewedUI()
		view.shell.agentsCollapsed = test.collapsed
		var frame bytes.Buffer
		if err := view.paint(&frame, test.width, 32); err != nil {
			t.Fatal(err)
		}
		screen := vt.NewEmulator(test.width, 32)
		if _, err := screen.Write(frame.Bytes()); err != nil {
			t.Fatal(err)
		}
		uisnapshot.Assert(t, "testdata/snapshots/orchestration-navigation-"+test.name+".txt", screen.String()+"\n")
		limit := 12
		if test.collapsed {
			limit = 0
		}
		rows := view.agents.nativeRoster(test.width, limit, now, true)
		uisnapshot.AssertTerminal(t, "testdata/snapshots/orchestration-agents-"+test.name+"-styles.txt", append(rows, "plain after roster"), test.width)
		screen.Close()
	}
}

func TestUISnapshotOrchestrationWorkingRoster(t *testing.T) {
	u, launch := orchestrateIdentityPendingTurn(t)
	orchestrateTestReply(t, u, launch, `{"turn":{"id":"batch-turn"}}`)
	v := u.navigation.views["child"]
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	for _, view := range []*appServerUI{u, v} {
		view.clock = func() time.Time { return now }
		view.view.clock, view.agents.clock = view.clock, view.clock
		view.view.painter.Theme, view.agents.painter.Theme = livediff.DarkTheme, livediff.DarkTheme
		view.modelsLoading = true
	}
	// Completed successful reads count once; failed reads do not count.
	orchestrateTestMessage(t, u, `{"method":"item/completed","params":{"threadId":"child","turnId":"batch-turn","item":{"id":"skill","type":"commandExecution","command":"skills-mgr get golang-best-practices","status":"completed","exitCode":0}}}`)
	orchestrateTestMessage(t, u, `{"method":"item/completed","params":{"threadId":"child","turnId":"batch-turn","item":{"id":"failed-skill","type":"commandExecution","command":"skills-mgr get goutils","status":"failed","exitCode":1}}}`)
	orchestrateTestMessage(t, u, `{"method":"turn/completed","params":{"threadId":"child","turn":{"id":"batch-turn","status":"completed"}}}`)
	for i := range 22 {
		thread := fmt.Sprintf("%02d-worker", i)
		appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": thread, "parentThreadId": "child", "agentNickname": thread}})
		if i > 0 && i < 9 {
			appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": thread, "turn": map[string]any{"id": "working"}})
		}
	}
	u.switchOrchestratedThread("child")
	v.agents.rosterSelected = "/root/08-worker"
	for _, test := range []struct {
		name          string
		height, focus int
	}{
		{"working", 40, 0}, {"scrolled", 40, 3}, {"short", 16, 3},
	} {
		v.shell.focus = test.focus
		v.shell.paintedRows = nil
		v.agents.rosterManual, v.agents.rosterOffset = test.focus == 3, 8
		var frame bytes.Buffer
		if err := v.paint(&frame, 100, test.height); err != nil {
			t.Fatal(err)
		}
		screen := vt.NewEmulator(100, test.height)
		_, _ = screen.Write(frame.Bytes())
		uisnapshot.Assert(t, "testdata/snapshots/orchestration-roster-"+test.name+".txt", screen.String()+"\n")
		screen.Close()
		shown := make(map[string]bool)
		for _, hit := range v.agents.hits {
			shown[hit.agent] = true
		}
		if !shown["/Orchestration/main"] {
			t.Fatal("Main scrolled out of the roster")
		}
		if test.height == 40 {
			for _, row := range v.agents.nativeRosterRows() {
				if row.agent.Responding && !shown[row.agent.Name] {
					t.Fatal("working row hidden", row.agent.Name)
				}
			}
		}
		boundary := v.shell.layout.rosterHorizontal
		u.switchOrchestratedThread("main")
		frame.Reset()
		if err := u.paint(&frame, 100, test.height); err != nil {
			t.Fatal(err)
		}
		if test.name == "working" {
			screen := vt.NewEmulator(100, test.height)
			_, _ = screen.Write(frame.Bytes())
			uisnapshot.Assert(t, "testdata/snapshots/orchestration-roster-switched-main.txt", screen.String()+"\n")
			screen.Close()
		}
		if u.shell.layout.rosterHorizontal != boundary || u.agents.rosterSelected != "/Orchestration/main" {
			t.Fatal("switch moved the roster boundary or retained the old active thread")
		}
		u.switchOrchestratedThread("child")
		if v.agents.rosterSelected != "/Orchestration/batch" || !v.shell.openSkills(v.agents, "/Orchestration/batch") {
			t.Fatal("return switch lost the active thread or completed skill reads")
		}
		v.shell.output = nil
	}
	v.shell.rosterHeight, v.shell.agentsCollapsed = 6, true
	u.switchOrchestratedThread("main")
	if u.shell.rosterHeight != 6 || !u.shell.agentsCollapsed {
		t.Fatal("switch lost the user's roster size or collapse state")
	}
}

func TestUISnapshotOrchestrationRosterActivityMetrics(t *testing.T) {
	u, launch := orchestrateIdentityPendingTurn(t)
	orchestrateTestReply(t, u, launch, `{"turn":{"id":"batch-turn"}}`)
	v := u.navigation.views["child"]
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	for i, view := range []*appServerUI{u, v} {
		view.clock = func() time.Time { return now }
		view.view.clock, view.agents.clock = view.clock, view.clock
		view.view.painter.Theme, view.agents.painter.Theme = livediff.DarkTheme, livediff.DarkTheme
		orchestrateTestMessage(t, u, fmt.Sprintf(`{"method":"turn/started","params":{"threadId":%q,"turn":{"id":"active"}}}`, view.thread))
		orchestrateTestMessage(t, u, fmt.Sprintf(`{"method":"item/started","params":{"threadId":%q,"turnId":"active","item":{"id":"run","type":"commandExecution","command":"go test ./...","status":"inProgress"}}}`, view.thread))
		view.shell.diff.callerCounts = map[string]livediff.Counts{"/root": {Added: 12 + i, Removed: 3 + i}}
		agent := view.session.agent("/root")
		agent.InputTokens, agent.OutputTokens = uint64(1200*(i+1)), uint64(240*(i+1))
		agent.TokensKnown, agent.CostKnown, agent.Cost = true, true, .02*float64(i+1)
		agent.UsagePartial, agent.CostPartial = i == 0, i == 0
		agent.ContextKnown, agent.ContextTokens, agent.ContextWindow = true, uint64(12000*(i+1)), 285000
		agent.OutputThroughput.OutputTokens, agent.OutputThroughput.DurationNanos, agent.OutputThroughput.MeasuredRequests = 170, uint64(10*time.Second), 1
		orchestrateTestMessage(t, u, fmt.Sprintf(`{"method":"thread/started","params":{"thread":{"id":%q,"parentThreadId":%q,"agentNickname":"worker"}}}`, view.thread+"-worker", view.thread))
		worker := view.session.agent("/root/worker")
		worker.InputTokens, worker.OutputTokens, worker.TokensKnown = 300, 60, true
		worker.Cost, worker.CostKnown = .01, true
		view.agents.apply(activityPaneEvent{Kind: "agents", Agents: view.session.agents})
	}
	orchestrateTestMessage(t, u, `{"method":"thread/started","params":{"thread":{"id":"extra","parentThreadId":"child","agentNickname":"extra"}}}`)
	orchestrateTestMessage(t, u, `{"method":"turn/started","params":{"threadId":"extra","turn":{"id":"working"}}}`)
	var header, status string
	for _, view := range []*appServerUI{u, v} {
		u.switchOrchestratedThread(view.thread)
		rows := view.agents.nativeRoster(160, 8, now, true)
		if header != "" && rows[0] != header {
			t.Fatal("switch changed run-wide header", rows[0], header)
		}
		header = rows[0]
		bar := view.shell.nativeStatus()
		if status != "" && bar != status {
			t.Fatal("switch changed run-wide Agents badge", bar, status)
		}
		status = bar
		rows = append(rows, bar)
		uisnapshot.Assert(t, "testdata/snapshots/orchestration-roster-activity-"+view.thread+".txt", strings.Join(rows, "\n")+"\n")
		for _, agent := range view.agents.orchestration {
			if got := ansi.Strip(view.agents.agentState(agent, 80, now)); !strings.Contains(got, "go test") {
				t.Fatalf("thread activity = %q", got)
			}
		}
		if view.agents.rosterLines["/Orchestration/main"].Added != 12 || view.agents.rosterLines["/Orchestration/batch"].Added != 13 {
			t.Fatalf("thread edit counts = %v", view.agents.rosterLines)
		}
	}
	// Updating one root shares one total easing clock across both shells.
	v.session.agent("/root").InputTokens += 1000
	v.orchestrationRoster()
	v.agents.nativeRoster(160, 8, now, true)
	now = now.Add(rosterMetricEase / 2)
	header = v.agents.nativeRoster(160, 8, now, true)[0]
	u.switchOrchestratedThread("main")
	if got := u.agents.nativeRoster(160, 8, now, true)[0]; got != header {
		t.Fatal("switch restarted total easing", got, header)
	}
}

func TestAppServerOrchestrateNavigationRepaint(t *testing.T) {
	u, launch := orchestrateIdentityPendingTurn(t)
	orchestrateTestReply(t, u, launch, `{"turn":{"id":"batch-turn"}}`)
	v := u.navigation.views["child"]
	u.modelsLoading, v.modelsLoading = true, true
	for _, thread := range []string{"main", "child", "main"} {
		u.switchOrchestratedThread(thread)
		var frame bytes.Buffer
		if err := u.viewedUI().paint(&frame, 100, 36); err != nil {
			t.Fatal(err)
		}
		if rows := strings.Count(frame.String(), "\x1b[2K"); rows != 36 {
			t.Fatalf("switch to %s repainted only %d/36 terminal rows", thread, rows)
		}
	}
}

func TestAppServerOrchestrateNavigationBTW(t *testing.T) {
	u, launch := orchestrateIdentityPendingTurn(t)
	orchestrateTestReply(t, u, launch, `{"turn":{"id":"batch-turn"}}`)
	orchestrateTestMessage(t, u, `{"method":"turn/completed","params":{"threadId":"child","turn":{"id":"batch-turn","status":"completed"}}}`)
	orchestrateTestReply(t, u, btwTestRequest(t, u.client.Input.(*appServerTestInput), "thread/inject_items", "main"), `{}`)
	v := u.navigation.views["child"]
	v.draft = "/btw side question"
	if err := v.shell.key('\r'); err != nil {
		t.Fatal(err)
	}
	w := u.client.Input.(*appServerTestInput)
	fork := btwTestRequest(t, w, "thread/fork", "child")
	btwTestReply(t, u, fork, `{"thread":{"id":"batch-side"}}`)
	start := btwTestRequest(t, w, "turn/start", "batch-side")
	btwTestReply(t, u, start, `{"turn":{"id":"side-turn"}}`)
	orchestrateTestMessage(t, u, `{"method":"turn/started","params":{"threadId":"batch-side","turn":{"id":"side-turn"}}}`)
	if v.btw.thread != "batch-side" || v.btw.turn != "side-turn" || !u.orchestrateBusy() {
		t.Fatal("batch side-thread lifecycle was lost")
	}
	if err := v.closeBTW(); err != nil {
		t.Fatal(err)
	}
	interrupt := btwTestRequest(t, w, "turn/interrupt", "batch-side")
	btwTestReply(t, u, interrupt, `{}`)
	unsubscribe := btwTestRequest(t, w, "thread/unsubscribe", "batch-side")
	btwTestReply(t, u, unsubscribe, `{}`)
	if len(v.btwRequests) != 0 || u.orchestrateBusy() {
		t.Fatal("side-thread cancellation did not settle")
	}
}

func TestAppServerOrchestrateNavigationExitInput(t *testing.T) {
	for _, shell := range []bool{false, true} {
		t.Run(fmt.Sprint(shell), func(t *testing.T) {
			u, launch := orchestrateIdentityPendingTurn(t)
			orchestrateTestReply(t, u, launch, `{"turn":{"id":"batch-turn"}}`)
			v := u.navigation.views["child"]
			message, method := "Unacknowledged batch steer", "turn/steer"
			if shell {
				message, method = "!echo unacknowledged shell", "thread/shellCommand"
			}
			v.draft = message
			if err := v.shell.key('\r'); err != nil {
				t.Fatal(err)
			}
			btwTestRequest(t, u.client.Input.(*appServerTestInput), method, "child")
			var out bytes.Buffer
			if err := u.finishOrchestratedViews(&out); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "outcome unknown (main › batch)") || !strings.Contains(out.String(), strings.TrimPrefix(message, "!")) {
				t.Fatal("exit omitted unsettled batch input", out.String())
			}
		})
	}
}

func TestAppServerOrchestrateNavigationClosedAdmission(t *testing.T) {
	replay, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	u, launch := orchestrateIdentityPendingTurnWithReplay(t, replay)
	orchestrateTestReply(t, u, launch, `{"turn":{"id":"batch-turn"}}`)
	second := *u.orchestrateThreads["child"]
	second.batch.TaskName = "second"
	copyLaunch := *second.batch.Launch
	copyLaunch.ThreadID = "child2"
	second.batch.Launch = &copyLaunch
	u.orchestrateThreads["child2"] = &second
	raw := []byte(fmt.Sprintf(`{"thread":{"id":"child2","cwd":%q}}`, second.batch.Cwd))
	if err := u.addOrchestratedView(&second, second.batch.Launch.ThreadID, raw, func() {}); err != nil {
		t.Fatal(err)
	}
	u.closeOrchestratedViews()
	if err := u.closeOrchestrateStorage(); err != nil {
		t.Fatal(err)
	}
	lock := flock.New(filepath.Join(replay.directory, strings.TrimSuffix(storageSessionName("child2"), ".json")+".lock"))
	ok, err := lock.TryLock()
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		lock.Unlock()
	}
	if !ok {
		t.Fatal("late admission retained a closed view's storage lease")
	}
}

func TestAppServerOrchestrationAgentsPanel(t *testing.T) {
	u, launch := orchestrateIdentityPendingTurn(t)
	orchestrateTestReply(t, u, launch, `{"turn":{"id":"batch-turn"}}`)
	u.orchestrationRoster()
	n := u.navigation
	v := n.views["child"]
	u.agents.agents = append(u.agents.agents, activityPaneAgent{Name: "/root/worker"})
	u.agents.selected, u.agents.only = "/root/worker", true
	for index, want := range []int{0, 1, 2, 4, 3} {
		for _, key := range []byte{2, byte('1' + index)} {
			if err := u.shell.key(key); err != nil {
				t.Fatal(err)
			}
		}
		if u.shell.focus != want {
			t.Fatalf("pane %d focused %d, want %d", index+1, u.shell.focus, want)
		}
	}
	for _, key := range []byte{2, '6'} {
		if err := u.shell.key(key); err != nil {
			t.Fatal(err)
		}
	}
	if u.shell.focus != 3 {
		t.Fatal("removed selector changed focus")
	}
	if err := u.shell.send("c"); err != nil {
		t.Fatal(err)
	}
	if !u.shell.agentsCollapsed {
		t.Fatal("Agents did not collapse")
	}
	u.agents.rosterSelected = "/Orchestration/main"
	if err := u.shell.send("j"); err != nil {
		t.Fatal(err)
	}
	if u.agents.rosterSelected != "/Orchestration/batch" || u.agents.selected != "/root/worker" || !u.agents.only || u.viewedUI() != u {
		t.Fatal("thread cursor changed native filter or switched early")
	}
	if err := u.shell.send("\r"); err != nil {
		t.Fatal(err)
	}
	if u.viewedUI() != v || v.shell.focus != 3 {
		t.Fatal("thread pick lost focus")
	}
	v.agents.rosterSelected = "/Orchestration/main"
	if err := v.shell.send("\r"); err != nil {
		t.Fatal(err)
	}
	if u.viewedUI() != u || u.agents.selected != "/root/worker" || !u.agents.only || !u.shell.agentsCollapsed {
		t.Fatal("switch lost native filter or collapse")
	}
	if err := u.paint(&bytes.Buffer{}, 120, 40); err != nil {
		t.Fatal(err)
	}
	r := u.shell.layout.roster
	if r.h != 1 {
		t.Fatal("collapsed panel did not retain one header")
	}
	for _, suffix := range []string{"M", "m"} {
		if err := u.shell.mouse(fmt.Sprintf("\x1b[<0;1;%d%s", r.y+1, suffix)); err != nil {
			t.Fatal(err)
		}
	}
	if u.shell.agentsCollapsed || u.shell.focus != 3 {
		t.Fatal("header did not expand Agents")
	}
	for _, report := range []string{fmt.Sprintf("\x1b[<0;1;%dM", r.y+1), fmt.Sprintf("\x1b[<32;1;%dM", r.y-1), fmt.Sprintf("\x1b[<0;1;%dm", r.y-1)} {
		if err := u.shell.mouse(report); err != nil {
			t.Fatal(err)
		}
	}
	if u.shell.rosterHeight < 2 || u.shell.agentsCollapsed {
		t.Fatal("Agents resize failed")
	}
	if err := u.shell.send("\x1b"); err != nil {
		t.Fatal(err)
	}
	if u.shell.focus != 0 {
		t.Fatal("Escape did not restore Main focus")
	}
	u.shell.diffOpen = true
	if err := u.paint(&bytes.Buffer{}, 120, 40); err != nil {
		t.Fatal(err)
	}
	for _, hit := range u.agents.hits {
		if hit.agent == "/root/worker" && !hit.skills {
			r := u.shell.layout.roster
			if err := u.shell.mouse(fmt.Sprintf("\x1b[<0;%d;%dM", hit.first, r.y+hit.row)); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	if u.agents.only || u.shell.diffOpen || u.viewedUI() != u {
		t.Fatal("native click did not toggle local Activity")
	}
	for i := range 6 {
		batch := u.orchestrateThreads["child"].batch
		batch.TaskName = fmt.Sprintf("extra_%d", i)
		n.retained = append(n.retained, batch)
	}
	u.shell.rosterHeight = 0
	u.orchestrationRoster()
	u.agents.rosterSelected = "/Orchestration/batch"
	if err := u.paint(&bytes.Buffer{}, 120, 40); err != nil {
		t.Fatal(err)
	}
	r = u.shell.layout.roster
	if err := u.shell.mouse(fmt.Sprintf("\x1b[<0;1;%dM", r.y+r.h)); err != nil {
		t.Fatal(err)
	}
	if u.viewedUI() != u {
		t.Fatal("overflow indicator switched without row hit")
	}
	filter, selected := u.agents.only, u.agents.selected
	if err := u.shell.mouse(fmt.Sprintf("\x1b[<65;1;%dM", r.y+2)); err != nil {
		t.Fatal(err)
	}
	if !u.agents.rosterManual || filter != u.agents.only || selected != u.agents.selected {
		t.Fatal("wheel changed filter")
	}
	for _, report := range []string{fmt.Sprintf("\x1b[<0;1;%dM", r.y+1), fmt.Sprintf("\x1b[<32;1;%dM", u.shell.height), fmt.Sprintf("\x1b[<0;1;%dm", u.shell.height)} {
		if err := u.shell.mouse(report); err != nil {
			t.Fatal(err)
		}
	}
	rows := u.shell.rosterHeight
	if err := u.shell.mouse("\x1b[<35;1;1M"); err != nil {
		t.Fatal(err)
	}
	if u.shell.rosterPress != nil || u.shell.rosterHeight != rows {
		t.Fatal("release retained roster drag")
	}
}
