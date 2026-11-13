package launch

import (
	"context"
	"fmt"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/backends/codex/appserver"

	"github.com/yusing/mekugi/libraries/diffrender"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
)

func TestUISnapshotLogicalWaiting(t *testing.T) {
	u, threads, at := nativeWaitUI(t, new(int64(1000)))
	u.attached.cwd = "/workspace"
	appServerTestNotify(t, u, "thread/status/changed", map[string]any{"threadId": "main", "status": map[string]any{"type": "idle"}})
	*at = at.Add(8 * time.Second)
	rows, _ := u.testMainFrame(80, 8, 0)
	view := u.attached.presentation.View()
	if view.Status.Composer.Text != "Waiting for agents" || !view.InterruptAvailable || !u.Busy(false) || view.Status.Session.CompletionKnown || at.Sub(view.Status.Session.Started) != 10*time.Second {
		t.Fatalf("native wait lost work: %+v", view.Status)
	}
	agent := u.attached.session.Agent("/root")
	if agent.Final || !agent.Waiting || agent.WorkTimer.At(*at) != 10*time.Second {
		t.Fatalf("native roster: %+v", agent)
	}
	assertNativeUISnapshot(t, "logical-waiting", rows)
	u.testActivity(u.shell.Agents).publishActivity()
	if text := ansi.Strip(strings.Join(u.shell.Agents.NativeRoster(120, 5, *at, true), "\n")); !strings.Contains(text, "Waiting for worker (timeout 1s)") {
		t.Fatalf("roster lost wait details: %s", text)
	}
	assertNativeUISnapshot(t, "logical-waiting-roster", u.shell.Agents.NativeRoster(80, 5, *at, true))
	appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "main", "turn": map[string]any{"id": "first", "status": "interrupted"}})
	root := threads.native.agent("main")
	root.Work.Disposition, root.Wait = "paused", nil
	nativeTreeFixture(t, threads)
	*at = at.Add(time.Hour)
	u.testMainFrame(80, 8, 0)
	if u.Busy(false) || u.attached.presentation.View().Status.Session.Running || u.attached.session.Agent("/root").WorkTimer.At(*at) != 10*time.Second {
		t.Fatal("paused native work revived")
	}
}

func TestUISnapshotUsageLimitWaiting(t *testing.T) {
	previous := time.Local
	time.Local = time.UTC
	t.Cleanup(func() { time.Local = previous })
	for _, mode := range []string{"true", "false", "capacity"} {
		t.Run(mode, func(t *testing.T) {
			u, threads, at := nativeRunningUI(t)
			*at = at.Add(2 * time.Second)
			u.attached.cwd = "/workspace"
			wait := appserver.AgentWaitObservation{WaitID: "wait", Mode: "any", Phase: "registered", Reason: new("providerUsageLimit")}
			if mode == "true" {
				wait.DeadlineUnixMS = new(at.Add(time.Hour).UnixMilli())
			}
			if mode == "capacity" {
				wait.Reason = new("providerCapacity")
				wait.DeadlineUnixMS = new(at.Add(time.Second).UnixMilli())
			}
			observe := func() {
				t.Helper()
				root := threads.native.agent("main")
				root.Work.Disposition, root.Wait = "waiting", &wait
				nativeTreeFixture(t, threads)
			}
			observe()
			rows, _ := u.testMainFrame(80, 8, 0)
			view := u.attached.presentation.View()
			if !view.InterruptAvailable || !view.Status.Session.Running || view.Status.Feedback.Alert || view.Status.Session.CompletionKnown {
				t.Fatalf("lost native provider wait: %+v", view)
			}
			assertNativeUISnapshot(t, "usage-limit-waiting-"+mode, rows)
			u.testActivity(u.shell.Agents).publishActivity()
			assertNativeUISnapshot(t, "usage-limit-roster-"+mode, u.shell.Agents.NativeRoster(80, 5, *at, true))
			if wait.DeadlineUnixMS != nil {
				root := threads.native.agent("main")
				root.Work.Disposition, root.Wait = "running", nil
				nativeTreeFixture(t, threads)
				u.testMainFrame(100, 8, 0)
				if strings.Contains(u.attached.presentation.View().Status.Session.Text, "retry at") {
					t.Fatal("running work retained retry deadline")
				}
				wait.DeadlineUnixMS = new(*wait.DeadlineUnixMS + 60000)
				observe()
				u.testMainFrame(100, 8, 0)
				if !strings.Contains(u.attached.presentation.View().Status.Session.Text, time.UnixMilli(*wait.DeadlineUnixMS).Format("15:04:05")) {
					t.Fatal("repeated rejection hid new native deadline")
				}
			}
			root := threads.native.agent("main")
			root.Work.Disposition, root.Wait = "interrupted", nil
			nativeTreeFixture(t, threads)
			appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "main", "turn": map[string]any{"id": "first", "status": "interrupted"}})
			u.testMainFrame(100, 8, 0)
			if u.attached.presentation.View().Status.Session.Running {
				t.Fatal("interrupted native provider wait remained running")
			}
		})
	}
}

func TestNativePausedProviderWaitProjection(t *testing.T) {
	for _, reason := range []string{"providerUsageLimit", ""} {
		t.Run(reason, func(t *testing.T) {
			u, threads, at := nativeRunningUI(t)
			*at = at.Add(2 * time.Second)
			root := threads.native.agent("main")
			root.Work.Disposition = "paused"
			root.Wait = &appserver.AgentWaitObservation{WaitID: "paused-wait", Phase: "registered"}
			want := "Paused"
			if reason != "" {
				root.Wait.Reason = new(reason)
				want = "Usage limit · paused"
			}
			nativeTreeFixture(t, threads)
			u.testMainFrame(100, 8, 0)
			view := u.attached.presentation.View()
			if view.Status.Composer.Text != want || view.Status.Session.Running || view.Status.Session.Animation != sessionpresentation.Static || !view.InterruptAvailable || view.Status.Session.CompletionKnown {
				t.Fatalf("paused provider fact became active, terminal or unavailable: %+v", view)
			}
			if u.Turn != "first" || u.Waiting != "paused-wait" || u.NativeWork != "main-work" {
				t.Fatalf("paused native authority was replaced: %+v", u.Live)
			}
			agent := u.attached.session.Agent("/root")
			if !agent.Paused || agent.Waiting || agent.Responding || agent.Wait == nil || agent.Wait.Live || agent.Wait.Body != want {
				t.Fatalf("paused roster became an ordinary active wait: %+v", agent)
			}
			assertAgentWork(t, u, "main", *at, 2*time.Second, false)
			root = threads.native.agent("main")
			root.Work.Disposition, root.Wait = "running", nil
			nativeTreeFixture(t, threads)
			u.testMainFrame(100, 8, 0)
			if view := u.attached.presentation.View(); view.Status.Composer.Text != "Working" || !view.Status.Session.Running || u.Waiting != "" {
				t.Fatalf("native running observation retained pause: %+v", view)
			}
		})
	}
}

func TestNativeChildPausedProviderWaitStaysScoped(t *testing.T) {
	main, threads, _ := nativeRunningUI(t)
	child := newAppServerSessionTestUI(t, main.attached.cwd)
	child.Thread, child.clock = "child", main.clock
	child.startSession("child", main.attached.cwd)
	child.attached.presentation.Attach(sessionpresentation.Thread{ID: "child", Caller: "/root"}, false)
	child.nativeAgent = threads.native.agent("child")
	threads.native.add(&nativeEndpoint{session: &child.nativeSession, before: func() error { return nil }, after: func() error { return nil }})
	appServerTestNotify(t, child, "turn/started", map[string]any{"threadId": "child", "turn": map[string]string{"id": "child-turn"}})
	agent := threads.native.agent("child")
	agent.Work.Disposition = "paused"
	agent.Wait = &appserver.AgentWaitObservation{WaitID: "child-wait", Reason: new("providerUsageLimit")}
	nativeTreeFixture(t, threads)
	child.testMainFrame(100, 8, 0)
	view := child.attached.presentation.View()
	if view.Status.Composer.Text != "Usage limit · paused" || view.Status.Session.Running || !view.InterruptAvailable || child.Turn != "child-turn" || child.NativeWork != "child-work" || child.Waiting != "child-wait" {
		t.Fatalf("child pause lost native identity or projection: %+v", view)
	}
	main.testMainFrame(100, 8, 0)
	if view := main.attached.presentation.View(); view.Status.Composer.Text != "Working" || !view.Status.Session.Running || main.Waiting != "" {
		t.Fatalf("child pause replaced Main's native work: %+v", view)
	}
}

func TestUISnapshotNativeRetryProgress(t *testing.T) {
	for _, thread := range []string{"main", "child"} {
		t.Run(thread, func(t *testing.T) {
			u, at := newActiveWorkTestUI(t)
			u.attached.cwd = "/workspace"
			u.shell.Transcript.Painter.Theme = diffrender.DarkTheme
			u.shell.Agents.Painter.Theme = diffrender.DarkTheme
			appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": thread, "turn": map[string]any{"id": "first"}})
			notify := func(turn, message string, retry bool) {
				appServerTestNotify(t, u, "error", map[string]any{"threadId": thread, "turnId": turn, "willRetry": retry,
					"error": map[string]any{"message": message, "additionalDetails": "Server overloaded", "codexErrorInfo": "serverOverloaded"}})
			}
			for _, message := range []string{"Reconnecting... 1/5", "Reconnecting... 2/5"} {
				notify("first", message, true)
				rows, _ := u.testMainFrame(100, 8, 0)
				view := u.attached.presentation.View()
				if thread == "main" && (!strings.Contains(view.Status.Composer.Text, message) || view.Status.Composer.Alert || !view.InterruptAvailable) {
					t.Fatalf("retry lost from shared status: %+v", view.Status)
				}
				if got := u.attached.session.RetryStatus(thread); !strings.Contains(got, message) || !strings.Contains(got, "Server overloaded") {
					t.Fatalf("retry facts lost: %q", got)
				}
				if message == "Reconnecting... 2/5" {
					if thread == "main" {
						assertNativeUISnapshot(t, "native-retry-status", rows)
					}
					u.testActivity(u.shell.Agents).publishActivity()
					assertNativeUISnapshot(t, "native-retry-roster-"+thread, u.shell.Agents.NativeRoster(100, 6, *at, true))
				}
			}
			notify("old", "stale retry", true)
			appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": thread, "turn": map[string]any{"id": "old", "status": "completed"}})
			if !strings.Contains(u.attached.session.RetryStatus(thread), "2/5") {
				t.Fatal("stale turn changed current retry")
			}
			appServerTestNotify(t, u, "item/agentMessage/delta", map[string]any{"threadId": thread, "turnId": "first", "itemId": "answer", "delta": "Recovered"})
			if u.attached.session.RetryStatus(thread) != "" {
				t.Fatal("response did not clear retry")
			}
			for _, outcome := range []string{"completed", "interrupted", "failed"} {
				appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": thread, "turn": map[string]any{"id": outcome}})
				notify(outcome, "Reconnecting... 1/5", true)
				appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": thread, "turn": map[string]any{"id": outcome, "status": outcome}})
				notify(outcome, "late retry", true)
				if u.attached.session.RetryStatus(thread) != "" {
					t.Fatalf("%s retained or reopened retry", outcome)
				}
			}
			notify("first", "terminal error", false)
			if u.attached.session.RetryStatus(thread) != "" {
				t.Fatal("terminal error became retry progress")
			}
		})
	}
}

func TestLogicalWaitContinuationPreservesElapsed(t *testing.T) {
	u, threads, at := nativeWaitUI(t, new(int64(1000)))
	*at = at.Add(8 * time.Second)
	nativeWaitItemFixture(t, u, "item/completed", new(int64(1000)))
	root := threads.native.agent("main")
	root.Work.Disposition, root.Wait = "running", nil
	nativeTreeFixture(t, threads)
	*at = at.Add(3 * time.Second)
	u.testMainFrame(80, 8, 0)
	if got := at.Sub(u.attached.presentation.View().Status.Session.Started); got != 13*time.Second {
		t.Fatalf("elapsed reset across native wake: %v", got)
	}
}

func TestNativeProviderFailureDiagnosticKeepsOriginalWork(t *testing.T) {
	for _, thread := range []string{"main", "child"} {
		t.Run(thread, func(t *testing.T) {
			u, _, _ := nativeRunningUI(t)
			if thread == "child" {
				appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": thread, "turn": map[string]any{"id": "first"}})
			}
			notify := func(turn, message string, retry bool) {
				appServerTestNotify(t, u, "error", map[string]any{"threadId": thread, "turnId": turn, "willRetry": retry, "error": map[string]any{"message": message, "additionalDetails": "Exact provider diagnostic"}})
			}
			notify("old", "Stale failure", false)
			notify("first", "Reconnecting... 1/5", true)
			notify("first", "Reconnecting... 2/5", true)
			for _, entry := range u.attached.childSource.Entries {
				if entry.Source.Kind == "error" {
					t.Fatal("stale failure or retry became a diagnostic row")
				}
			}
			for range 2 {
				notify("first", "Provider stream failed", false)
			}
			count := 0
			for _, entry := range u.attached.childSource.Entries {
				if entry.Source.Kind == "error" {
					count++
					if entry.Source.ErrorDetail != "Provider stream failed · Exact provider diagnostic" || entry.Source.Native.Turn != "first" {
						t.Fatalf("provider evidence changed: %+v", entry)
					}
				}
			}
			if count != 1 || u.Turn != "first" || u.attached.session.RetryStatus(thread) != "" || u.attached.session.Lifecycle(thread).Final {
				t.Fatalf("provider diagnostic duplicated, retried or completed original work: count=%d turn=%s", count, u.Turn)
			}
			view := u.testActivity(u.shell.Agents)
			if thread == "main" {
				view = u.testActivity(u.shell.Transcript)
			}
			paint := ansi.Strip(strings.Join(view.testRenderConversation(120).Lines, "\n"))
			if !strings.Contains(paint, "Provider stream failed") {
				t.Fatalf("provider failure absent from rendered consumer: %s", paint)
			}
		})
	}
}

func TestSupersededLocalWaitRestoresWorkingRoster(t *testing.T) {
	u, threads, at := nativeWaitUI(t, nil)
	nativeWaitSettlementFixture(t, u, appserver.CoordinationWaitResult{Kind: "cancelled", Cause: &appserver.CoordinationWaitCancellation{Kind: "inputSuperseded", DeliveryID: "human-input"}}, nil, nil)
	root := threads.native.agent("main")
	root.Work.Disposition, root.Wait = "running", nil
	nativeTreeFixture(t, threads)
	agent := u.attached.session.Agent("/root")
	if agent.Waiting || agent.Wait != nil || agent.WaitingFor != "" || !agent.Responding || u.LocalStop != "" {
		t.Fatalf("running native work retained wait: %+v", agent)
	}
	u.testActivity(u.shell.Agents).publishActivity()
	if text := ansi.Strip(strings.Join(u.shell.Agents.NativeRoster(120, 5, *at, true), "\n")); strings.Contains(text, "Waiting for") {
		t.Fatalf("stale wait in roster: %s", text)
	}
	published := u.attached.childSource.Publication(u.attached.presentation, u.attached.source)
	*at = at.Add(time.Second)
	threads.native.projectAgent(&u.nativeSession)
	if u.attached.childSource.Publication(u.attached.presentation, u.attached.source) != published {
		t.Fatal("unchanged native work republished roster")
	}
}

func TestLogicalWaitHumanCancellation(t *testing.T) {
	u, threads, _ := nativeWaitUI(t, nil)
	wire := u.Host.Input.(*appServerTestInput)
	if c := nativeTestCancel(t, u); c.quit {
		t.Fatal("wait cancellation quit client")
	}
	request := appServerOneRequest(t, wire, "turn/interrupt", "")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{}}`, request.ID))
	if !u.Busy(false) || u.Waiting == "" {
		t.Fatal("interrupt ack became terminal work")
	}
	root := threads.native.agent("main")
	root.Work.Disposition, root.Wait = "interrupted", nil
	nativeTreeFixture(t, threads)
	appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "main", "turn": map[string]any{"id": "first", "status": "interrupted"}})
	if u.NativeWork != "" || u.Waiting != "" || u.Busy(false) {
		t.Fatal("native interrupted work remained busy")
	}
}

func TestUISnapshotLogicalPausedChildren(t *testing.T) {
	u, threads, at := nativeWaitUI(t, new(int64(1000)))
	threads.native.agent("main").Work.Disposition = "paused"
	threads.native.agent("child").Work.Disposition = "paused"
	nativeTreeFixture(t, threads)
	u.testActivity(u.shell.Agents).publishActivity()
	launcher := &nativeReattachLauncher{run: func(ctx context.Context, start sessionapi.Start, api *sessionapi.Server) error {
		frame := sessionapi.NativeFrame{View: u.attached.presentation.View(), Main: u.attached.source.Publication(u.attached.presentation, nil), Agents: u.attached.childSource.Publication(u.attached.presentation, u.attached.source)}
		root, members := threads.native.family("main")
		if err := publishNativeFamilySource(api, frame, start.Workspace, root, "/root", "", members); err != nil {
			return err
		}
		if err := api.SetSummary(sessionapi.Summary{Thread: "main", Workspace: start.Workspace, Ready: true}); err != nil {
			return err
		}
		<-ctx.Done()
		return ctx.Err()
	}}
	hub := sessionapi.NewHub(t.Context(), launcher)
	defer hub.Close()
	server := httptest.NewServer(sessionapi.Versioned(hub.Handler()))
	defer server.Close()
	rpc, err := sessionapi.OpenRPC(t.Context(), server.Client(), server.URL+sessionapi.RPCPath, "")
	if err != nil {
		t.Fatal(err)
	}
	defer rpc.Close()
	serverRPCCall(t, rpc, "session/start", "", sessionapi.Start{Workspace: u.attached.cwd, Model: "fixture", Resume: "main"})
	family, _ := hub.Family("main")
	if family == nil || !slices.Contains(family.Threads, "child") {
		t.Fatal("paused child was not retained as a cold runtime destination")
	}
	found := false
	for _, row := range family.Agents.Roster {
		if row.Agent.Thread == "child" {
			found = row.Agent.Paused && !row.Agent.Responding && !row.Agent.Waiting
		}
	}
	if !found {
		t.Fatal("cold family child lost its paused work metadata")
	}
	u.shell.Agents.UpdateRoster(family.Agents)
	assertNativeUISnapshot(t, "logical-paused-children", u.shell.Agents.NativeRoster(80, 5, *at, true))
}

func TestRecoveryInputRetriesWithoutAnotherHostMessage(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.NativeInputPending = true
	u.attached.conversation.Unsent = []conversationmodel.Input{{Text: "queued during recovery"}}
	if err := u.nativeSession.flushInput(&u.attached, &u.picker, u.clock, &u.Editor, t.Context()); err != nil {
		t.Fatal(err)
	}
	if wire.Len() != 0 || len(u.attached.conversation.Unsent) != 1 {
		t.Fatal("input bypassed native admission")
	}
	u.NativeInputPending = false
	if err := u.nativeSession.flushInput(&u.attached, &u.picker, u.clock, &u.Editor, t.Context()); err != nil {
		t.Fatal(err)
	}
	appServerOneRequest(t, wire, "turn/start", "queued during recovery")
}

func TestLogicalWaitKeepsQueuedComposerInput(t *testing.T) {
	u, threads, _ := nativeWaitUI(t, nil)
	wire := u.Host.Input.(*appServerTestInput)
	appServerTestKeys(t, u, "next assignment\t")
	if wire.Len() != 0 || len(u.attached.conversation.Queued) != 1 || u.Waiting != "wait" {
		t.Fatal("queued composer input superseded native wait")
	}
	if next := u.attached.presentation.NextInput(&u.Live, u.attached.conversation, nil, ""); len(next.Batch.Parts) != 0 {
		t.Fatal("ordinary input flushing drained queue during wait")
	}
	root := threads.native.agent("main")
	root.Work.Disposition, root.Wait = "running", nil
	nativeTreeFixture(t, threads)
	appServerTestTurn(t, u, "resumed")
	if next := u.attached.presentation.NextInput(&u.Live, u.attached.conversation, nil, ""); len(next.Batch.Parts) != 0 || len(u.attached.conversation.Queued) != 1 {
		t.Fatal("native wake promoted queued input into steer")
	}
	u.Live.Completed(true)
	threads.native.agent("main").Work.Disposition = "completed"
	nativeTreeFixture(t, threads)
	if next := u.attached.presentation.NextInput(&u.Live, u.attached.conversation, nil, ""); len(next.Batch.Parts) != 1 || next.Batch.Parts[0].Text != "next assignment" || next.Steer {
		t.Fatalf("queue did not release after native outcome: %+v", next)
	}
}

func TestChildCompactionDoesNotReserveAssignment(t *testing.T) {
	u, threads, _ := nativeWaitUI(t, nil)
	child := threads.native.agent("child")
	child.Work.Disposition = "completed"
	u.Thread, u.Turn, u.LocalStop, u.Waiting, u.NativeWork = "child", "", "", "", ""
	u.attached.threadInput = &threadInput{caller: "/root/worker", admitted: true}
	u.attached.conversation.Unsent = []conversationmodel.Input{{Text: "/compact", Compact: new(conversationmodel.CompactIntent{})}}
	if err := u.nativeSession.flushInput(&u.attached, &u.picker, u.clock, &u.Editor, t.Context()); err != nil {
		t.Fatal(err)
	}
	appServerOneRequest(t, u.Host.Input.(*appServerTestInput), "thread/compact/start", "")
	if child.Work.WorkID != "child-work" || child.Work.Disposition != "completed" {
		t.Fatal("Go compact changed native work snapshot")
	}
}

func TestUISnapshotLogicalWaitNoTimeout(t *testing.T) {
	u, threads, at := nativeWaitUI(t, nil)
	u.attached.cwd = "/workspace"
	previousWait := u.attached.session.Agent("/root").Wait
	*at = at.Add(time.Hour)
	threads.native.projectAgent(&u.nativeSession)
	if u.Waiting != "wait" {
		t.Fatal("deadline-free wait settled without native outcome")
	}
	agent := u.attached.session.Agent("/root")
	if previousWait == nil || agent.Wait != previousWait {
		t.Fatal("unchanged wait invalidated roster publication")
	}
	if agent.Responding || !agent.Waiting || agent.WaitingFor != "worker" {
		t.Fatalf("stopped parent remained active: %+v", agent)
	}
	rows, _ := u.testMainFrame(100, 10, 0)
	if text := ansi.Strip(strings.Join(rows, "\n")); !strings.Contains(text, "Waiting for worker") || strings.Contains(text, "timeout") {
		t.Fatalf("deadline-free wait: %s", text)
	}
	assertNativeUISnapshot(t, "logical-wait-no-timeout", rows)
	u.testActivity(u.shell.Agents).publishActivity()
	if text := ansi.Strip(strings.Join(u.shell.Agents.NativeRoster(120, 5, *at, true), "\n")); !strings.Contains(text, "Waiting for worker") || strings.Contains(text, "timeout") {
		t.Fatalf("roster differs from deadline-free transcript: %s", text)
	}
}

func TestLogicalWaitPublicationUsesNativeIdentity(t *testing.T) {
	u, _, _ := nativeWaitUI(t, nil)
	result := appserver.CoordinationWaitResult{Kind: "released", Wake: "events"}
	messages := []appserver.CoordinationWaitMessage{{DeliveryID: "message", SenderTaskName: new("/root/worker")}}
	nativeWaitSettlementFixture(t, u, result, nil, messages)
	nativeWaitSettlementFixture(t, u, result, nil, messages)
	var found []*sessionpresentation.NativeItem
	for _, entry := range u.attached.session.Activity().Main.Entries {
		if item := entry.Source.Native; item != nil && item.Wait != nil {
			found = append(found, item)
		}
	}
	if len(found) != 2 || found[0].Item != "wait-call" || found[1].Item != "native:wait:wait" || found[1].Turn != "first" || found[1].Wait.WaitReason != "message received" {
		t.Fatalf("native wait identity/reason duplicated or lost: %+v", found)
	}
}

func TestLogicalStoppedTargetPublishesWakeReason(t *testing.T) {
	for _, status := range []string{"paused", "failed", "completed", "interrupted"} {
		t.Run(status, func(t *testing.T) {
			u, threads, _ := nativeWaitUI(t, nil)
			nativeWaitSettlementFixture(t, u, appserver.CoordinationWaitResult{Kind: "released", Wake: "events"}, []appserver.CoordinationWaitOutcome{{Target: appserver.CoordinationTarget{ThreadID: "child", TaskName: "/root/worker", WorkID: "child-work"}, Disposition: status}}, nil)
			root := threads.native.agent("main")
			root.Wait = nil
			root.Work.Disposition = "running"
			nativeTreeFixture(t, threads)
			rows, _ := u.testMainFrame(100, 15, 0)
			if text := ansi.Strip(strings.Join(rows, "\n")); !strings.Contains(text, "Finished waiting for worker: "+status) || strings.Contains(text, status+": completed") {
				t.Fatalf("stopped-target wake invisible: %s", text)
			}
		})
	}
}

func TestUISnapshotLogicalWaitAllOutcome(t *testing.T) {
	for _, reason := range []string{"paused", "delivery failed"} {
		t.Run(reason, func(t *testing.T) {
			u, threads, _ := nativeWaitUI(t, nil)
			u.attached.cwd = "/workspace"
			root := threads.native.agent("main")
			root.Wait.Mode = "all"
			nativeTreeFixture(t, threads)
			result := appserver.CoordinationWaitResult{Kind: "released", Wake: "events"}
			var outcomes []appserver.CoordinationWaitOutcome
			if reason == "paused" {
				outcomes = []appserver.CoordinationWaitOutcome{{Target: appserver.CoordinationTarget{ThreadID: "child", TaskName: "/root/worker", WorkID: "child-work"}, Disposition: "paused"}}
			} else {
				result = appserver.CoordinationWaitResult{Kind: "cancelled", Cause: &appserver.CoordinationWaitCancellation{Kind: "dispatchRejected", Diagnostic: new("injection rejected")}}
			}
			nativeWaitSettlementFixture(t, u, result, outcomes, nil)
			root = threads.native.agent("main")
			root.Wait = nil
			root.Work.Disposition = "running"
			nativeTreeFixture(t, threads)
			rows, _ := u.testMainFrame(80, 10, 0)
			if text := ansi.Strip(strings.Join(rows, "\n")); !strings.Contains(text, "Finished waiting for worker: "+reason) || strings.Contains(text, reason+": completed") {
				t.Fatalf("aggregate wake invisible: %s", text)
			}
			name := "logical-wait-all-outcome"
			if reason == "delivery failed" {
				name += "-delivery-failed"
			}
			assertNativeUISnapshot(t, name, rows)
		})
	}
}

func TestStandaloneCompactionOmitsAgentWaitPresentation(t *testing.T) {
	u, threads, _ := nativeRunningUI(t)
	appServerTestNotify(t, u, "thread/contextReset", sessioncontrol.ContextReset{Thread: "main", Turn: "first", ResetID: "standalone", Status: "started"})
	nativeTreeFixture(t, threads)
	if u.attached.session.Lifecycle("main").Wait != nil {
		t.Fatal("standalone compact published targetless agent wait")
	}
	appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "main", "turn": map[string]any{"id": "continued"}})
	for _, entry := range u.attached.source.Entries {
		if entry.Source.Native != nil && entry.Source.Native.Wait != nil {
			t.Fatal("standalone continuation published agent-wait outcome")
		}
	}
}

func TestCompactionOutcomeSurvivesWaitCancellation(t *testing.T) {
	for _, outcome := range []string{"completed", "failed"} {
		t.Run(outcome, func(t *testing.T) {
			u, threads, _ := nativeWaitUI(t, nil)
			appServerTestKeys(t, u, "/compact\r")
			request := appServerOneRequest(t, u.Host.Input.(*appServerTestInput), "thread/compact/start", "")
			compactWaitReset(u, request, "", "accepted", 1)
			compactWaitReset(u, request, "compact", "started", 1)
			compactWaitReset(u, request, "compact", outcome, 1)
			nativeWaitSettlementFixture(t, u, appserver.CoordinationWaitResult{Kind: "cancelled", Cause: &appserver.CoordinationWaitCancellation{Kind: "inputSuperseded"}}, nil, nil)
			root := threads.native.agent("main")
			root.Wait = nil
			root.Work.Disposition = "running"
			nativeTreeFixture(t, threads)
			for _, entry := range u.attached.source.Entries {
				if strings.Contains(entry.Source.Text, "Compaction canceled") {
					t.Fatalf("settled reset mislabeled after wait cancellation: %q", entry.Source.Text)
				}
			}
		})
	}
}
