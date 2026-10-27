package launch

import (
	json "encoding/json/v2"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

type interruptedTrafficFixture struct {
	ui      *appServerUI
	threads *threadExecution
	session history.Session
	target  string
}

func trafficCheck(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func interruptedTrafficUI(t *testing.T, nested bool) interruptedTrafficFixture {
	t.Helper()
	u := newAppServerSessionTestUI(t, t.TempDir())
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	u.clock = func() time.Time { return now }
	retained, err := history.NewService(control.RecordKinds())
	trafficCheck(t, err)
	store, err := retained.Open(t.Context(), t.TempDir())
	trafficCheck(t, err)
	session, err := store.SelectSession(t.Context(), nil, history.Limits{Total: history.DefaultStorageBytes}, "main")
	trafficCheck(t, err)
	t.Cleanup(session.Close)
	s := control.New()
	trafficCheck(t, s.AdmitRoot(t.Context(), session, &control.Member{Thread: "main", Session: "main-session", Task: "/root", Generation: 1, Status: control.Working, AudienceAdmitted: true}))
	trafficCheck(t, s.RequestStarted(t.Context(), control.Identity{Thread: "main", Session: "main-session", Turn: "turn"}, true))
	parent, target := "main", "/root"
	names := []string{"worker"}
	if nested {
		names = []string{"coordinator", "worker"}
	}
	for _, name := range names {
		caller := control.Identity{Thread: parent, Session: parent + "-session", Turn: "turn", Call: "spawn-" + name, Item: "exec"}
		spawn, err := s.ReserveSpawn(t.Context(), caller, control.SpawnRequest{Name: name, Message: "Work", Settings: control.Settings{Workspace: u.attached.cwd}})
		trafficCheck(t, err)
		for _, phase := range []string{"create", "assignment_input", "admit", "initial"} {
			effect, err := s.ClaimEffect(t.Context(), parent, spawn.ID)
			trafficCheck(t, err)
			if effect == nil || effect.Phase != phase {
				t.Fatalf("spawn phase %s: %+v", phase, effect)
			}
			trafficCheck(t, s.EffectAcknowledged(t.Context(), parent, spawn.ID, control.Receipt{Phase: phase, Thread: name, Session: name + "-session", Turn: "turn"}))
		}
		trafficCheck(t, s.RequestStarted(t.Context(), control.Identity{Thread: name, Session: name + "-session", Turn: "turn"}, false))
		parent, target = name, target+"/"+name
	}
	return interruptedTrafficFixture{u, &threadExecution{shared: s}, session, target}
}

func (f interruptedTrafficFixture) request(t *testing.T, id string) binding.ThreadItem {
	t.Helper()
	caller := control.Identity{Thread: "main", Session: "main-session", Turn: "turn", Call: id, Item: "exec"}
	action, err := f.threads.shared.PrepareAction(t.Context(), caller, f.target, "interrupt", "")
	trafficCheck(t, err)
	claimed, err := f.threads.shared.ClaimEffect(t.Context(), "main", action.ID)
	trafficCheck(t, err)
	if claimed == nil {
		t.Fatal("interruption was not dispatched")
	}
	trafficCheck(t, f.threads.shared.EffectAcknowledged(t.Context(), "main", action.ID, control.Receipt{}))
	tree, err := f.threads.shared.Snapshot("main")
	trafficCheck(t, err)
	result, err := json.Marshal(map[string]string{"id": action.ID, "state": tree.Effect("main", action.ID).State, "thread_id": "worker", "task_name": f.target})
	trafficCheck(t, err)
	envelope, err := json.Marshal(map[string]any{"content": []map[string]string{{"type": "text", "text": string(result)}}})
	trafficCheck(t, err)
	args, err := json.Marshal(map[string]string{"target": f.target})
	trafficCheck(t, err)
	return binding.ThreadItem{ID: id, Type: "mcpToolCall", Server: "threadcontrol", Tool: "interrupt_agent", Status: "completed", Arguments: args, Result: envelope}
}

func (f interruptedTrafficFixture) observe(t *testing.T, item binding.ThreadItem) {
	t.Helper()
	appServerTestNotify(t, f.ui, "item/completed", map[string]any{"threadId": "main", "turnId": "turn", "item": item})
}

func (f interruptedTrafficFixture) complete(t *testing.T) {
	t.Helper()
	trafficCheck(t, f.threads.shared.HostCompleted(t.Context(), "worker", "turn", control.Interrupted, control.HostFailure{Message: "Status: interrupted\n"}))
	syncLogicalWork(&f.ui.attached, &f.ui.Live, f.threads, f.ui.clock())
}

func TestUISnapshotInterruptedTraffic(t *testing.T) {
	for _, theme := range []struct {
		name  string
		theme diffrender.Theme
	}{{"dark", diffrender.DarkTheme}, {"light", diffrender.LightTheme}} {
		t.Run(theme.name, func(t *testing.T) {
			f := interruptedTrafficUI(t, false)
			f.observe(t, f.request(t, "interrupt"))
			u := f.ui
			view := u.testActivity(u.shell.Transcript)
			view.Clock, view.Painter.Theme, view.Conversation = u.clock, theme.theme, true
			f.complete(t)
			publishNativeDelivery(&u.attached, control.NativeInput{Thread: "main", ID: "interrupted", Sender: "/root/worker", Recipient: "/root", Kind: "FINAL_ANSWER", Outcome: control.Interrupted, Message: "Status: interrupted\n"}, u.clock())
			for _, status := range []control.Status{control.Failed, control.Completed} {
				publishNativeDelivery(&u.attached, control.NativeInput{Thread: "main", ID: string(status), Sender: map[control.Status]string{control.Failed: "/root/failure", control.Completed: "/root/success"}[status], Recipient: "/root", Kind: "FINAL_ANSWER", Outcome: status, Message: "Result evidence."}, u.clock())
			}
			settleActivity(u.clock().Add(time.Second), view)
			rows := view.testRenderConversation(90).Lines
			text := ansi.Strip(strings.Join(rows, "\n"))
			if strings.Contains(text, "Interrupt requested") || !strings.Contains(text, "Interrupted /root/worker") || !strings.Contains(text, "interrupted") || !strings.Contains(text, "failed") || !strings.Contains(text, "finished") {
				t.Fatalf("terminal outcomes did not settle: %s", text)
			}
			if entry := u.attached.source.Entries[0]; entry.Source.Native.ToolDetail == "" {
				t.Fatal("settlement discarded original request evidence")
			}
			uisnapshot.AssertTerminal(t, fmt.Sprintf("testdata/snapshots/interrupted-traffic-%s.txt", theme.name), append(rows, "plain after traffic"), 90)
		})
	}
}

func TestInterruptedTrafficSettlementOrdering(t *testing.T) {
	for _, mode := range []string{"outcome-first", "non-parent", "restored"} {
		t.Run(mode, func(t *testing.T) {
			f := interruptedTrafficUI(t, mode == "non-parent")
			item := f.request(t, "interrupt")
			if mode == "non-parent" {
				f.observe(t, item)
			}
			f.complete(t)
			if mode == "restored" {
				restored := control.New()
				trafficCheck(t, restored.Restore(t.Context(), f.session))
				clock := f.ui.clock
				f.ui = newAppServerSessionTestUI(t, t.TempDir())
				f.ui.clock = clock
				f.threads = &threadExecution{shared: restored}
				syncLogicalWork(&f.ui.attached, &f.ui.Live, f.threads, f.ui.clock())
			}
			if mode != "non-parent" {
				f.observe(t, item)
			}
			view := f.ui.testActivity(f.ui.shell.Transcript)
			settleActivity(f.ui.clock().Add(time.Second), view)
			text := ansi.Strip(strings.Join(view.testRenderConversation(90).Lines, "\n"))
			if strings.Contains(text, "Interrupt requested") || !strings.Contains(text, "Interrupted "+f.target) {
				t.Fatalf("%s did not settle: %s", mode, text)
			}
		})
	}
}

func TestInterruptedTrafficIgnoresPriorGenerationOutcome(t *testing.T) {
	f := interruptedTrafficUI(t, false)
	f.observe(t, f.request(t, "interrupt"))
	f.complete(t)
	caller := control.Identity{Thread: "main", Session: "main-session", Turn: "turn", Call: "restart", Item: "exec"}
	action, err := f.threads.shared.PrepareAction(t.Context(), caller, f.target, "followup", "Again")
	trafficCheck(t, err)
	_, err = f.threads.shared.ClaimEffect(t.Context(), "main", action.ID)
	trafficCheck(t, err)
	trafficCheck(t, f.threads.shared.EffectAcknowledged(t.Context(), "main", action.ID, control.Receipt{Turn: "next"}))
	trafficCheck(t, f.threads.shared.RequestStarted(t.Context(), control.Identity{Thread: "worker", Session: "worker-session", Turn: "next"}, false))
	f.observe(t, f.request(t, "new-interrupt"))
	trafficCheck(t, f.threads.shared.HostCompleted(t.Context(), "worker", "turn", control.Interrupted, control.HostFailure{Message: "old"}))
	publishNativeDelivery(&f.ui.attached, control.NativeInput{Thread: "main", ID: "old", Sender: f.target, Recipient: "/root", Kind: "FINAL_ANSWER", Outcome: control.Interrupted, SourceGeneration: 1, ParentTurn: "turn"}, f.ui.clock())
	syncLogicalWork(&f.ui.attached, &f.ui.Live, f.threads, f.ui.clock())
	entry := f.ui.attached.source.Entries[1].Source
	if entry.Text != "Interrupt requested for "+f.target {
		t.Fatalf("old result settled new request: %s", entry.Text)
	}
	trafficCheck(t, f.threads.shared.HostCompleted(t.Context(), "worker", "next", control.Interrupted, control.HostFailure{}))
	syncLogicalWork(&f.ui.attached, &f.ui.Live, f.threads, f.ui.clock())
	if entry := f.ui.attached.source.Entries[1].Source; entry.Text != "Interrupted "+f.target {
		t.Fatalf("current result did not settle new request: %s", entry.Text)
	}
}

func TestCompletionGuardSkippedContinuationSettlesRoster(t *testing.T) {
	f := interruptedTrafficUI(t, false)
	appServerTestNotify(t, f.ui, "turn/started", map[string]any{"threadId": "main", "turn": map[string]any{"id": "turn"}})
	trafficCheck(t, f.threads.shared.HostCompleted(t.Context(), "main", "turn", control.Completed, control.HostFailure{}))
	syncLogicalWork(&f.ui.attached, &f.ui.Live, f.threads, f.ui.clock())
	appServerTestNotify(t, f.ui, "turn/completed", map[string]any{"threadId": "main", "turn": map[string]any{"id": "turn", "status": "completed"}})
	trafficCheck(t, f.threads.shared.HostCompleted(t.Context(), "worker", "turn", control.Completed, control.HostFailure{}))
	m, _ := f.threads.shared.Member("main")
	_, err := f.threads.shared.ClaimEffect(t.Context(), "main", m.Stop.Wait)
	trafficCheck(t, err)
	syncLogicalWork(&f.ui.attached, &f.ui.Live, f.threads, f.ui.clock())
	m, _ = f.threads.shared.Member("main")
	life := f.ui.attached.session.Lifecycle("main")
	f.ui.testMainFrame(80, 8, 0)
	if m.Status != control.Completed || !life.Final || f.ui.Busy(false) || !f.ui.attached.presentation.View().Status.Session.CompletionKnown {
		t.Fatalf("logical=%s roster.Final=%t Waiting=%t Responding=%t", m.Status, life.Final, life.Waiting, life.Responding)
	}
}

func TestCompletionGuardWaitsForExactReduction(t *testing.T) {
	for _, scenario := range []string{"completion-queued", "start-and-completion-queued", "continuation-acknowledged", "child-already-settled"} {
		t.Run(scenario, func(t *testing.T) {
			f := interruptedTrafficUI(t, false)
			turn := "turn"
			if scenario != "completion-queued" {
				trafficCheck(t, f.threads.shared.HostCompleted(t.Context(), "main", turn, control.Interrupted, control.HostFailure{}))
				turn = "next"
			}
			if scenario == "child-already-settled" {
				trafficCheck(t, f.threads.shared.HostCompleted(t.Context(), "worker", "turn", control.Completed, control.HostFailure{}))
			}
			appServerTestNotify(t, f.ui, "turn/started", map[string]any{"threadId": "main", "turn": map[string]any{"id": turn}})
			syncLogicalWork(&f.ui.attached, &f.ui.Live, f.threads, f.ui.clock())
			params, err := json.Marshal(map[string]any{"threadId": "main", "turn": map[string]any{"id": turn, "status": "completed"}})
			trafficCheck(t, err)
			message := appserver.Message{Method: "turn/completed", Params: params}
			reduced := make(chan error, 1)
			deferred, err := deferLogicalCompletion(&f.ui.attached, &f.ui.Live, message, reduced)
			trafficCheck(t, err)
			if !deferred {
				t.Fatal("completion bypassed its pending logical reduction")
			}
			dispatch := func(m appserver.Message) error {
				syncLogicalWork(&f.ui.attached, &f.ui.Live, f.threads, f.ui.clock())
				return f.ui.message(m)
			}
			trafficCheck(t, f.ui.attached.threadInput.flushCompletions(dispatch))
			syncLogicalWork(&f.ui.attached, &f.ui.Live, f.threads, f.ui.clock())
			f.ui.testMainFrame(80, 8, 0)
			if f.ui.attached.presentation.View().Status.Session.CompletionKnown || !f.ui.Busy(false) {
				t.Fatal("pending reduction published an idle or completed parent")
			}
			// Commit the queued start and exact terminal disposition, then expose
			// the receipt consumed by the ordinary host notification path.
			if turn == "next" {
				trafficCheck(t, f.threads.shared.RequestStarted(t.Context(), control.Identity{Thread: "main", Session: "main-session", Turn: turn}, true))
			}
			trafficCheck(t, f.threads.shared.HostCompleted(t.Context(), "main", turn, control.Completed, control.HostFailure{}))
			if scenario == "continuation-acknowledged" {
				member, _ := f.threads.shared.Member("main")
				effect, err := f.threads.shared.ClaimEffect(t.Context(), "main", member.Stop.Wait)
				trafficCheck(t, err)
				trafficCheck(t, f.threads.shared.EffectAcknowledged(t.Context(), "main", effect.ID, control.Receipt{Turn: "continued"}))
			}
			close(reduced)
			trafficCheck(t, f.ui.attached.threadInput.flushCompletions(dispatch))
			syncLogicalWork(&f.ui.attached, &f.ui.Live, f.threads, f.ui.clock())
			f.ui.testMainFrame(80, 8, 0)
			if len(f.ui.attached.threadInput.completions) != 0 {
				t.Fatal("consumed completion remained retained")
			}
			if scenario == "child-already-settled" {
				if f.ui.Busy(false) || !f.ui.attached.presentation.View().Status.Session.CompletionKnown || !f.ui.attached.session.Lifecycle("main").Final {
					t.Fatal("confirmed completion failed to settle")
				}
				return
			}
			if !f.ui.Busy(false) || f.ui.attached.presentation.View().Status.Session.CompletionKnown {
				t.Fatal("guarded completion released active work")
			}
			if scenario == "continuation-acknowledged" {
				appServerTestNotify(t, f.ui, "turn/started", map[string]any{"threadId": "main", "turn": map[string]any{"id": "continued"}})
				syncLogicalWork(&f.ui.attached, &f.ui.Live, f.threads, f.ui.clock())
				if !f.ui.Busy(false) || f.ui.LocalStop != "" {
					t.Fatal("continued host turn failed to retire confirmed stop")
				}
				return
			}
			_, err = f.threads.shared.HumanInterrupt(t.Context(), "main", f.ui.Waiting)
			trafficCheck(t, err)
			syncLogicalWork(&f.ui.attached, &f.ui.Live, f.threads, f.ui.clock())
			if member, _ := f.threads.shared.Member("main"); member.Status != control.Interrupted || f.ui.Busy(false) {
				t.Fatal("explicit interruption failed to settle guarded work")
			}
		})
	}
}

func TestCompletionDeferralPreservesExplicitHostOutcomes(t *testing.T) {
	for _, status := range []control.Status{control.Interrupted, control.Failed} {
		t.Run(string(status), func(t *testing.T) {
			f := interruptedTrafficUI(t, false)
			appServerTestNotify(t, f.ui, "turn/started", map[string]any{"threadId": "main", "turn": map[string]any{"id": "turn"}})
			syncLogicalWork(&f.ui.attached, &f.ui.Live, f.threads, f.ui.clock())
			params, err := json.Marshal(map[string]any{"threadId": "main", "turn": map[string]any{"id": "turn", "status": status}})
			trafficCheck(t, err)
			message := appserver.Message{Method: "turn/completed", Params: params}
			deferred, err := deferLogicalCompletion(&f.ui.attached, &f.ui.Live, message, make(chan error, 1))
			trafficCheck(t, err)
			if deferred {
				t.Fatal("explicit terminal outcome was deferred")
			}
			trafficCheck(t, f.ui.message(message))
			if f.ui.Busy(false) {
				t.Fatal("explicit outcome left host work busy")
			}
		})
	}
}

func TestFailedCompletionReductionDoesNotPublishSuccess(t *testing.T) {
	f := interruptedTrafficUI(t, false)
	appServerTestNotify(t, f.ui, "turn/started", map[string]any{"threadId": "main", "turn": map[string]any{"id": "turn"}})
	syncLogicalWork(&f.ui.attached, &f.ui.Live, f.threads, f.ui.clock())
	params, err := json.Marshal(map[string]any{"threadId": "main", "turn": map[string]any{"id": "turn", "status": "completed"}})
	trafficCheck(t, err)
	reduced := make(chan error, 1)
	deferred, err := deferLogicalCompletion(&f.ui.attached, &f.ui.Live, appserver.Message{Method: "turn/completed", Params: params}, reduced)
	trafficCheck(t, err)
	if !deferred {
		t.Fatal("completion was not deferred")
	}
	reduced <- errors.New("checkpoint unavailable")
	close(reduced)
	trafficCheck(t, f.ui.attached.threadInput.flushCompletions(func(appserver.Message) error {
		t.Fatal("failed checkpoint published successful completion")
		return nil
	}))
	if len(f.ui.attached.threadInput.completions) != 0 {
		t.Fatal("failed receipt blocked later completion observations")
	}
}
