package router

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
	"github.com/gofrs/flock"
	"github.com/yusing/mekugi/internal/ui/diffview"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

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
	for _, hit := range v.navigation.roster.hits {
		if hit.agent == "/Orchestration/main" && !hit.skills {
			r := v.shell.layout.orchestration
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
	v.navigation.roster.selected = "/Orchestration/main"
	v.shell.focus = 5
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
		view.status, view.model = "Ready", "snapshot-model"
		view.turn, view.awaitingTurn = "", false
		view.modelsLoading = true
		view.shell.focus = 5
		view.agents.apply(activityPaneEvent{Kind: "agents", Agents: view.session.agents})
	}
	for _, thread := range []string{"main", "child"} {
		orchestrateTestMessage(t, u, fmt.Sprintf(`{"method":"item/completed","params":{"threadId":%q,"turnId":"turn","item":{"id":"read","type":"commandExecution","command":"cat %s.go","status":"completed","exitCode":0}}}`, thread, thread))
	}
	for _, view := range []*appServerUI{u, v} {
		view.shell.journalOpen = false
	}
	u.draft, v.draft = "Review the batch", "Complete the batch"
	u.agents.agents = append(u.agents.agents, activityPaneAgent{Name: "/root/main_worker"})
	v.agents.agents = append(v.agents.agents, activityPaneAgent{Name: "/root/batch_worker"})
	for _, test := range []struct {
		name, thread string
		collapsed    bool
	}{{"main", "main", false}, {"batch", "child", false}, {"collapsed", "main", true}} {
		u.switchOrchestratedThread(test.thread)
		view := u.viewedUI()
		u.navigation.collapsed = test.collapsed
		view.shell.agentsCollapsed = test.collapsed
		var frame bytes.Buffer
		if err := view.paint(&frame, 100, 32); err != nil {
			t.Fatal(err)
		}
		screen := vt.NewEmulator(100, 32)
		if _, err := screen.Write(frame.Bytes()); err != nil {
			t.Fatal(err)
		}
		uisnapshot.Assert(t, "testdata/snapshots/orchestration-navigation-"+test.name+".txt", screen.String()+"\n")
		screen.Close()
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
	if err := u.addOrchestratedView(&second, raw, func() {}); err != nil {
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

func TestAppServerOrchestrationPanels(t *testing.T) {
	u, launch := orchestrateIdentityPendingTurn(t)
	orchestrateTestReply(t, u, launch, `{"turn":{"id":"batch-turn"}}`)
	u.orchestrationRoster()
	n := u.navigation
	u.agents.selected = "/root/worker"
	for index, want := range []int{0, 1, 2, 4, 3, 5} {
		digit := byte('1' + index)
		if err := u.shell.key(2); err != nil {
			t.Fatal(err)
		}
		if err := u.shell.key(digit); err != nil {
			t.Fatal(err)
		}
		if u.shell.focus != want {
			t.Fatalf("pane %c focused %d, want %d", digit, u.shell.focus, want)
		}
	}
	if err := u.shell.send("c"); err != nil {
		t.Fatal(err)
	}
	if !n.collapsed || u.shell.agentsCollapsed {
		t.Fatal("Orchestration collapse changed Agents")
	}
	u.shell.selectNativePane(3)
	if err := u.shell.send("c"); err != nil {
		t.Fatal(err)
	}
	if !n.collapsed || !u.shell.agentsCollapsed {
		t.Fatal("Agents collapse changed Orchestration")
	}
	u.shell.selectNativePane(5)
	n.roster.selected = "/Orchestration/batch"
	if err := u.shell.send("\r"); err != nil {
		t.Fatal(err)
	}
	v := u.viewedUI()
	if !n.collapsed || v.shell.agentsCollapsed {
		t.Fatal("collapse state crossed thread owners")
	}
	n.roster.selected = "/Orchestration/main"
	if err := v.shell.send("\r"); err != nil {
		t.Fatal(err)
	}
	if u.agents.selected != "/root/worker" || !u.shell.agentsCollapsed {
		t.Fatal("thread switching lost native roster state")
	}
	if err := u.paint(&bytes.Buffer{}, 120, 40); err != nil {
		t.Fatal(err)
	}
	r := u.shell.layout.orchestration
	if r.h != 1 || u.shell.layout.roster.h != 1 {
		t.Fatal("collapsed rosters did not retain separate headers", u.shell.layout)
	}
	if err := u.shell.mouse(fmt.Sprintf("\x1b[<0;%d;%dM", r.x+1, r.y+1)); err != nil {
		t.Fatal(err)
	}
	if err := u.shell.mouse(fmt.Sprintf("\x1b[<0;%d;%dm", r.x+1, r.y+1)); err != nil {
		t.Fatal(err)
	}
	if n.collapsed || !u.shell.agentsCollapsed || u.shell.focus != 5 {
		t.Fatal("header click did not independently expand Orchestration")
	}
	for _, report := range []string{fmt.Sprintf("\x1b[<0;1;%dM", r.y+1), fmt.Sprintf("\x1b[<32;1;%dM", r.y-1), fmt.Sprintf("\x1b[<0;1;%dm", r.y-1)} {
		if err := u.shell.mouse(report); err != nil {
			t.Fatal(err)
		}
	}
	if n.height < 2 || n.collapsed || u.shell.rosterHeight != 0 {
		t.Fatal("Orchestration drag changed another panel or collapsed")
	}
	if err := u.shell.send("\x1b"); err != nil {
		t.Fatal(err)
	}
	if u.shell.focus != 0 {
		t.Fatal("Escape did not restore Main focus")
	}
	for i := range 6 {
		batch := u.orchestrateThreads["child"].batch
		batch.TaskName = fmt.Sprintf("extra_%d", i)
		n.retained = append(n.retained, batch)
	}
	n.height = 0
	u.orchestrationRoster()
	n.roster.selected = "/Orchestration/batch"
	if err := u.paint(&bytes.Buffer{}, 120, 40); err != nil {
		t.Fatal(err)
	}
	r = u.shell.layout.orchestration
	if err := u.shell.mouse(fmt.Sprintf("\x1b[<0;1;%dM", r.y+r.h)); err != nil {
		t.Fatal(err)
	}
	if u.viewedUI() != u {
		t.Fatal("overflow indicator switched a thread without a row hit")
	}
	for _, report := range []string{fmt.Sprintf("\x1b[<0;1;%dM", r.y+1), fmt.Sprintf("\x1b[<32;1;%dM", u.shell.height), fmt.Sprintf("\x1b[<0;1;%dm", u.shell.height)} {
		if err := u.shell.mouse(report); err != nil {
			t.Fatal(err)
		}
	}
	rows := n.height
	if err := u.shell.mouse("\x1b[<35;1;1M"); err != nil {
		t.Fatal(err)
	}
	if u.shell.rosterPress != nil || n.height != rows {
		t.Fatal("release over status bar retained roster drag")
	}

}
