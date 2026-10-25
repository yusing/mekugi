package control

import (
	"context"
	json "encoding/json/v2"
	"path"
	"path/filepath"
	"testing"
	"time"

	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

func fixture(t *testing.T) (*Service, history.Session) {
	t.Helper()
	retained, err := history.NewService(RecordKinds())
	if err != nil {
		t.Fatal(err)
	}
	store, err := retained.Open(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	session, err := store.SelectSession(t.Context(), nil, history.Limits{Total: history.DefaultStorageBytes}, "root")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(session.Close)
	service := New()
	root := Member{Thread: "root", Session: "root-session", Task: "/root", Generation: 1, Status: Working, AudienceAdmitted: true, Settings: Settings{Workspace: t.TempDir(), RuntimeRoots: []string{"/admitted"}}, Limits: Limits{MinWaitMS: 5, MaxWaitMS: 100}}
	must(t, service.AdmitRoot(t.Context(), session, &root))
	hostStarted(t, service, "root", "rt")
	return service, session
}

func TestRestoreBeforeAnyRequestLeavesChildrenCold(t *testing.T) {
	s, session := fixture(t)
	child(t, s, "root", "worker", "/root/child-runtime", "wt")
	for _, target := range []string{"child-runtime", "/root/child-runtime"} {
		member, err := s.Target(identity("root", "rt"), target)
		must(t, err)
		if member.Thread != "worker" {
			t.Fatalf("hyphenated target resolved to %+v", member)
		}
	}
	restored := New()
	must(t, restored.Restore(t.Context(), session))
	root, ok := restored.Member("root")
	if !ok || root.Status != Paused || root.HostRunning || root.Turn != "" {
		t.Fatalf("root was not restored paused: %+v", root)
	}
	members := restored.Descendants("root")
	if len(members) != 1 || members[0].Task != "/root/child-runtime" || members[0].Status != Paused {
		t.Fatalf("cold roster unavailable before input: %+v", members)
	}
	fresh, err := session.Store.SelectSession(t.Context(), nil, session.Limits, "fresh")
	must(t, err)
	defer fresh.Close()
	must(t, restored.Restore(t.Context(), fresh))
	if _, ok := restored.Member("fresh"); ok {
		t.Fatal("restore invented a fresh root's execution authority")
	}
}

func TestRestoreIgnoresLegacyDelegationLimits(t *testing.T) {
	_, session := fixture(t)
	name := recordName("root")
	data, err := history.ReadRecord(filepath.Join(session.Store.Directory(), name))
	must(t, err)
	var checkpoint map[string]any
	must(t, json.Unmarshal(data, &checkpoint))
	root := checkpoint["Members"].(map[string]any)["root"].(map[string]any)
	root["Limits"].(map[string]any)["Depth"] = 0
	root["Limits"].(map[string]any)["Concurrency"] = 1
	data, err = json.Marshal(checkpoint)
	must(t, err)
	must(t, session.Store.Transaction(t.Context(), session.Writes, func(tx *history.Transaction) error {
		return tx.Write(name, "legacy-depth-", data)
	}))
	restored := New()
	must(t, restored.Restore(t.Context(), session))
	hostStarted(t, restored, "root", "resumed")
	child(t, restored, "root", "worker", "/root/worker", "wt")
	child(t, restored, "worker", "nested", "/root/worker/nested", "nt")
	child(t, restored, "root", "second", "/root/second", "st")
	child(t, restored, "root", "third", "/root/third", "tt")
}

func message(t *testing.T, s *Service, caller Identity, text string) {
	t.Helper()
	action, err := s.PrepareAction(t.Context(), caller, "/root", "message", text)
	must(t, err)
	_, err = s.ClaimEffect(t.Context(), caller.Thread, action.ID)
	must(t, err)
	must(t, s.EffectAcknowledged(t.Context(), caller.Thread, action.ID, Receipt{}))
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func child(t *testing.T, s *Service, parent, id, task, turn string) {
	t.Helper()
	state, err := s.Snapshot(parent)
	must(t, err)
	caller := identity(parent, state.Members[parent].Turn)
	caller.Call = "spawn-" + id
	reservation, err := s.ReserveSpawn(t.Context(), caller, SpawnRequest{Name: path.Base(task), Settings: Settings{Workspace: t.TempDir(), RuntimeRoots: []string{"/admitted"}}, Limits: Limits{MinWaitMS: 5, MaxWaitMS: 100}, Message: "Work."})
	must(t, err)
	for _, stage := range []string{"create", "assignment_input", "admit", "initial"} {
		receipt := Receipt{}
		if stage == "create" {
			receipt = Receipt{Thread: id, Session: id + "-session"}
		}
		if stage == "initial" {
			receipt.Turn = turn
		}
		effect, err := s.ClaimEffect(t.Context(), parent, reservation.ID)
		must(t, err)
		if effect == nil || effect.Phase != stage {
			t.Fatalf("creation phase %s = %+v", stage, effect)
		}
		receipt.Phase = stage
		must(t, s.EffectAcknowledged(t.Context(), parent, reservation.ID, receipt))
	}
	hostStarted(t, s, id, turn)
}
func identity(thread, turn string) Identity {
	return Identity{Thread: thread, Session: thread + "-session", Turn: turn, Call: thread + "-wait-call", Item: thread + "-exec"}
}
func stop(t *testing.T, s *Service, id Identity) {
	t.Helper()
	must(t, s.Execution(t.Context(), id.Thread, id.Turn, id.Item, "", true))
	ok, err := s.ClaimLocalStop(t.Context(), id.Thread, id.Turn, id.Item)
	must(t, err)
	if ok == nil {
		t.Fatal("local stop not admitted")
	}
	must(t, s.HostCompleted(t.Context(), id.Thread, id.Turn, Completed, ""))
}
func deliver(t *testing.T, s *Service, thread string) {
	t.Helper()
	d, err := claimPending(t, s, t.Context(), thread)
	must(t, err)
	if d == nil {
		t.Fatal("no result injection intent")
	}
	must(t, s.EffectAcknowledged(t.Context(), thread, d.ID, Receipt{}))
}

func TestNestedStopWaitPreservesLogicalWork(t *testing.T) {
	s, _ := fixture(t)
	ctx := t.Context()
	now := time.Unix(100, 0)
	child(t, s, "root", "child", "/root/child", "ct")
	child(t, s, "child", "grandchild", "/root/child/grandchild", "gt")
	root, childID := identity("root", "rt"), identity("child", "ct")
	for _, id := range []Identity{root, childID} {
		r, err := s.RegisterWait(ctx, id, nil, true, nil, now)
		must(t, err)
		if r.State != "registered" {
			t.Fatal(r)
		}
	}
	stop(t, s, childID)
	stop(t, s, root)
	state, err := s.Snapshot("root")
	must(t, err)
	if len(effectsOf(state, "final")) != 0 || len(deliveries(state)) != 0 || state.Members["child"].Status != Waiting || !state.Members["child"].Reserved() {
		t.Fatalf("local stop settled child: %+v", state)
	}
	must(t, s.AnswerCompleted(ctx, "grandchild", "gt", "grandchild result"))
	must(t, s.HostCompleted(ctx, "grandchild", "gt", Completed, ""))
	if wake, err := claimWake(t, s, ctx, "child", now); err != nil || wake != nil {
		t.Fatalf("completion woke before result injection: %v, %v", wake, err)
	}
	deliver(t, s, "grandchild")
	wake, err := claimWake(t, s, ctx, "child", now)
	must(t, err)
	if wake == nil || wake.Event.Source != "grandchild" {
		t.Fatal(wake)
	}
	must(t, s.EffectAcknowledged(ctx, "child", wake.ID, Receipt{Turn: "ct2"}))
	if again, err := claimWake(t, s, ctx, "child", now); err != nil || again != nil {
		t.Fatalf("duplicate child wake: %v, %v", again, err)
	}
	must(t, s.AnswerCompleted(ctx, "child", "ct2", "ordinary child result"))
	must(t, s.HostCompleted(ctx, "child", "ct2", Completed, ""))
	deliver(t, s, "child")
	parentWake, err := claimWake(t, s, ctx, "root", now)
	must(t, err)
	if parentWake == nil || parentWake.Event.Source != "child" {
		t.Fatal(parentWake)
	}
	state, err = s.Snapshot("root")
	must(t, err)
	if len(effectsOf(state, "final")) != 2 || effectsOf(state, "final")[1].Message != "ordinary child result" || state.Members["child"].Reserved() {
		t.Fatalf("incorrect terminal reduction: %+v", state)
	}
}

func TestWaitAnyConsumesOnlyUnseenEvents(t *testing.T) {
	s, _ := fixture(t)
	child(t, s, "root", "a", "/root/a", "at")
	child(t, s, "root", "b", "/root/b", "bt")
	action, err := s.PrepareAction(t.Context(), identity("a", "at"), "/root", "message", "progress already read")
	must(t, err)
	_, err = s.ClaimEffect(t.Context(), "a", action.ID)
	must(t, err)
	foreign := identity("root", "rt")
	foreign.Session = "foreign"
	if err := s.InputConsumed(t.Context(), foreign, []string{MessageItemID("a:a-wait-call")}); err == nil {
		t.Fatal("foreign session consumed an attributed notification")
	}
	must(t, s.InputConsumed(t.Context(), identity("root", "rt"), []string{MessageItemID("a:a-wait-call")}))
	// The host can expose injected input before its acknowledgement is reduced.
	must(t, s.EffectAcknowledged(t.Context(), "a", action.ID, Receipt{}))
	items := []string{MessageItemID("a:a-wait-call")}
	caller := identity("root", "rt")
	if allocations := testing.AllocsPerRun(10, func() { err = s.InputConsumed(t.Context(), caller, items) }); err != nil || allocations != 0 {
		t.Fatalf("unchanged input copied the retained tree: allocations=%v err=%v", allocations, err)
	}
	wait, err := s.RegisterWait(t.Context(), caller, []string{"a", "b"}, false, nil, time.Now())
	must(t, err)
	if wait.State != "registered" || wait.Event != nil {
		t.Fatalf("already read progress woke a new wait: %+v", wait)
	}
	stop(t, s, caller)
	message(t, s, identity("b", "bt"), "new progress")
	wake, err := claimWake(t, s, t.Context(), "root", time.Now())
	must(t, err)
	if wake == nil || wake.Event.Kind != "message" || wake.Event.Source != "b" || wake.Event.Status != Working {
		t.Fatalf("legitimate mailbox wake lost identity: %+v", wake)
	}
	must(t, s.EffectAcknowledged(t.Context(), "root", wake.ID, Receipt{Turn: "rt2"}))
	caller.Turn, caller.Call, caller.Item = "rt2", "second-wait", "second-exec"
	wait, err = s.RegisterWait(t.Context(), caller, []string{"a", "b"}, false, nil, time.Now())
	must(t, err)
	if wait.State != "registered" {
		t.Fatalf("consumed event was delivered twice: %+v", wait)
	}
	stop(t, s, caller)
	must(t, s.HostCompleted(t.Context(), "a", "at", Completed, ""))
	deliver(t, s, "a")
	wake, err = claimWake(t, s, t.Context(), "root", time.Now())
	must(t, err)
	if wake == nil || wake.Event.Kind != "completed" || wake.Event.Source != "a" {
		t.Fatalf("wait-any did not wake on one completion: %+v", wake)
	}
	b, _ := s.Member("b")
	if b.Status != Working || !b.HostRunning {
		t.Fatal("wait-any changed the unfinished child", b)
	}
}

func TestConsumedFinalStaysConsumedAfterRestart(t *testing.T) {
	s, session := fixture(t)
	child(t, s, "root", "a", "/root/a", "at")
	must(t, s.HostCompleted(t.Context(), "a", "at", Completed, ""))
	deliver(t, s, "a")
	tree, err := s.Snapshot("root")
	must(t, err)
	final := effectsOf(tree, "final")[0]
	must(t, s.InputConsumed(t.Context(), identity("root", "rt"), []string{MessageItemID(final.ID)}))
	restored := New()
	must(t, restored.Restore(t.Context(), session))
	hostStarted(t, restored, "root", "resumed")
	wait, err := restored.RegisterWait(t.Context(), identity("root", "resumed"), []string{"a"}, false, nil, time.Now())
	must(t, err)
	if wait.State != "registered" || wait.Event != nil {
		t.Fatalf("restart redelivered a result already read by the parent: %+v", wait)
	}
}

func TestWakeDispatchSurvivesTerminalAndColdRestore(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(map[bool]string{false: "cold", true: "terminal_before_ack"}[terminal], func(t *testing.T) {
			s, session := fixture(t)
			child(t, s, "root", "child", "/root/child", "ct")
			_, err := s.RegisterWait(t.Context(), identity("root", "rt"), nil, true, nil, time.Now())
			must(t, err)
			stop(t, s, identity("root", "rt"))
			message(t, s, identity("child", "ct"), "")
			wake, err := claimWake(t, s, t.Context(), "root", time.Now())
			must(t, err)
			if wake == nil {
				t.Fatal("missing wake")
			}
			wake.Event.Message = "caller mutation"
			if terminal {
				hostStarted(t, s, "root", "next")
				must(t, s.HostCompleted(t.Context(), "root", "next", Completed, ""))
				must(t, s.EffectAcknowledged(t.Context(), "root", wake.ID, Receipt{Uncertain: true}))
			} else {
				tree, err := s.Snapshot("root")
				must(t, err)
				restored := New()
				must(t, restored.AdmitRoot(t.Context(), session, tree.Members["root"]))
				s = restored
			}
			tree, err := s.Snapshot("root")
			must(t, err)
			if tree.Members["root"].Wait.State != "uncertain" || tree.waitResult(tree.Members["root"].Wait).Event.Message != "" {
				t.Fatal("retained dispatch was lost or aliased")
			}
			must(t, s.EffectAcknowledged(t.Context(), "root", wake.ID, Receipt{Reconciled: true}))
			tree, err = s.Snapshot("root")
			must(t, err)
			want := Paused
			if terminal {
				want = Completed
			}
			if tree.Members["root"].Status != want || tree.Members["root"].HostRunning {
				t.Fatal("reconciliation changed logical completion or revived the host")
			}
		})
	}
}

func TestWaitSelectionTimeoutAndExecutionAdmission(t *testing.T) {
	s, _ := fixture(t)
	ctx := t.Context()
	now := time.Unix(100, 0)
	child(t, s, "root", "a", "/root/a", "at")
	child(t, s, "root", "b", "/root/b", "bt")
	caller := identity("root", "rt")
	for _, target := range [][]string{{}, {"missing"}, {"/root"}, {"/foreign/child"}} {
		if _, err := s.RegisterWait(ctx, caller, target, false, nil, now); err == nil {
			t.Fatalf("accepted invalid selection %v", target)
		}
	}
	timeout := int64(101)
	if _, err := s.RegisterWait(ctx, caller, []string{"a"}, false, &timeout, now); err == nil {
		t.Fatal("accepted excessive timeout")
	}
	timeout = 1
	r, err := s.RegisterWait(ctx, caller, []string{"a"}, false, &timeout, now)
	must(t, err)
	state, err := s.Snapshot("root")
	must(t, err)
	w := state.Members["root"].Wait
	if *w.RequestedMS != 1 || *w.EffectiveMS != 5 || w.Deadline.Sub(now) != 5*time.Millisecond {
		t.Fatal(w)
	}
	for _, e := range []struct{ success, yield bool }{{false, false}, {true, true}} {
		must(t, s.Execution(ctx, "root", "rt", caller.Item, "", e.success && !e.yield))
		ok, err := s.ClaimLocalStop(ctx, "root", "rt", caller.Item)
		must(t, err)
		if ok != nil {
			t.Fatal("unfinished execution armed stop")
		}
	}
	message(t, s, identity("b", "bt"), "unselected")
	stop(t, s, caller)
	if wake, err := claimWake(t, s, ctx, "root", now); err != nil || wake != nil {
		t.Fatalf("unselected event woke: %v, %v", wake, err)
	}
	wake, err := claimWake(t, s, ctx, "root", now.Add(5*time.Millisecond))
	must(t, err)
	if wake == nil || wake.State != "timeout" || wake.ID != r.ID {
		t.Fatal(wake)
	}
	state, err = s.Snapshot("root")
	must(t, err)
	if deliveries(state)[0].ClaimedBy != "" {
		t.Fatal("unselected event consumed")
	}
}

func TestColdResumeDoesNotRearmOrRedispatch(t *testing.T) {
	s, session := fixture(t)
	ctx := t.Context()
	now := time.Unix(100, 0)
	child(t, s, "root", "a", "/root/a", "at")
	id := identity("root", "rt")
	_, err := s.RegisterWait(ctx, id, nil, true, nil, now)
	must(t, err)
	stop(t, s, id)
	must(t, s.AnswerCompleted(ctx, "a", "at", "result"))
	must(t, s.HostCompleted(ctx, "a", "at", Completed, ""))
	restored := New()
	must(t, restored.AdmitRoot(ctx, session, &Member{Thread: "root", Session: "root-session", Task: "/root"}))
	if d, err := claimPending(t, restored, ctx, "a"); err != nil || d != nil {
		t.Fatalf("resume redispatched result: %v %v", d, err)
	}
	if w, err := claimWake(t, restored, ctx, "root", now.Add(24*time.Hour)); err != nil || w != nil {
		t.Fatalf("resume rearmed wait: %v %v", w, err)
	}
	state, err := restored.Snapshot("root")
	must(t, err)
	if state.Members["root"].Status != Paused || state.Members["root"].Wait.State != "paused" {
		t.Fatal(state.Members["root"])
	}
}

func TestUncertainResultDoesNotCreateSuccessfulWake(t *testing.T) {
	s, _ := fixture(t)
	ctx := context.Background()
	child(t, s, "root", "a", "/root/a", "at")
	must(t, s.HostCompleted(ctx, "a", "at", Completed, ""))
	d, err := claimPending(t, s, ctx, "a")
	must(t, err)
	must(t, s.EffectAcknowledged(ctx, "a", d.ID, Receipt{Error: "acknowledgement lost", Uncertain: true}))
	if duplicate, err := claimPending(t, s, ctx, "a"); err != nil || duplicate != nil {
		t.Fatalf("uncertain result retried: %v %v", duplicate, err)
	}
	state, err := s.Snapshot("root")
	must(t, err)
	if len(deliveries(state)) != 0 || effectsOf(state, "final")[0].State != "uncertain" {
		t.Fatal(state)
	}
}

func TestRequestAdmissionReusesPublishedState(t *testing.T) {
	s, session := fixture(t)
	caller := identity("root", "rt")
	owner, err := s.owner("root")
	must(t, err)
	before := owner.state.Load()
	// Holding the store transaction proves retries do not attempt publication.
	must(t, session.Store.Transaction(t.Context(), session.Writes, func(*history.Transaction) error {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		return s.RequestStarted(ctx, caller, true)
	}))
	if owner.state.Load() != before {
		t.Fatal("repeated inference replaced the publication")
	}
	caller.Session = "foreign"
	if err := s.RequestStarted(t.Context(), caller, true); err == nil {
		t.Fatal("retry bypassed session authentication")
	}
	caller = identity("root", "rt")
	member, err := s.Caller(caller)
	must(t, err)
	member.Settings.RuntimeRoots[0] = "caller mutation"
	if owner.state.Load().Members["root"].Settings.RuntimeRoots[0] == member.Settings.RuntimeRoots[0] {
		t.Fatal("caller mutated the admitted member")
	}
	must(t, s.HostCompleted(t.Context(), "root", "rt", Completed, ""))
	caller.Turn = "next"
	must(t, s.RequestStarted(t.Context(), caller, true))
	if member := owner.state.Load().Members["root"]; member.Generation != 2 || member.Turn != "next" || !member.Reserved() {
		t.Fatal("new inference did not start the next work generation")
	}
}

func settle(t *testing.T, s *Service, caller, id string, receipt Receipt) {
	t.Helper()
	effect, err := s.ClaimEffect(t.Context(), caller, id)
	must(t, err)
	if effect == nil {
		t.Fatalf("effect %s was not dispatchable", id)
	}
	receipt.Phase = effect.Phase
	must(t, s.EffectAcknowledged(t.Context(), caller, id, receipt))
}

func effectsOf(tree Tree, kind string) []Effect {
	var effects []Effect
	for _, effect := range tree.Effects {
		if effect.Kind == kind {
			effects = append(effects, effect)
		}
	}
	return effects
}

func claimWake(t *testing.T, s *Service, ctx context.Context, thread string, now time.Time) (*WaitResult, error) {
	t.Helper()
	effect, err := s.PrepareWake(ctx, thread, now)
	if err != nil || effect == nil {
		return nil, err
	}
	claimed, err := s.ClaimEffect(ctx, thread, effect.ID)
	if err != nil || claimed == nil {
		return nil, err
	}
	return claimed.Wake, nil
}

func TestUnsentWakeIsSupersededByHumanAdmission(t *testing.T) {
	s, _ := fixture(t)
	child(t, s, "root", "child", "/root/child", "ct")
	_, err := s.RegisterWait(t.Context(), identity("root", "rt"), nil, true, nil, time.Now())
	must(t, err)
	stop(t, s, identity("root", "rt"))
	message(t, s, identity("child", "ct"), "selected message")
	wake, err := s.PrepareWake(t.Context(), "root", time.Now())
	must(t, err)
	if wake == nil {
		t.Fatal("missing prepared continuation")
	}
	must(t, s.RequestStarted(t.Context(), identity("root", "human"), true))
	claimed, err := s.ClaimEffect(t.Context(), "root", wake.ID)
	must(t, err)
	if claimed != nil {
		t.Fatal("superseded wake could start another host turn")
	}
	tree, err := s.Snapshot("root")
	must(t, err)
	if tree.Members["root"].Turn != "human" || deliveries(tree)[0].ClaimedBy != "" {
		t.Fatal("human admission lost its turn or consumed the unsent event")
	}
}

func TestFrozenCreationDoesNotBorrowCallerSettings(t *testing.T) {
	s, _ := fixture(t)
	settings := Settings{Workspace: t.TempDir(), RuntimeRoots: []string{"original"}}
	created, err := s.ReserveSpawn(t.Context(), identity("root", "rt"), SpawnRequest{Name: "child", Message: "Work.", Settings: settings})
	must(t, err)
	settings.RuntimeRoots[0] = "caller mutation"
	created.Child.Settings.RuntimeRoots[0] = "result mutation"
	tree, err := s.Snapshot("root")
	must(t, err)
	if tree.Effect("root", created.ID).Child.Settings.RuntimeRoots[0] != "original" {
		t.Fatal("published preparation retained caller-owned authority")
	}
}

func hostStarted(t *testing.T, s *Service, thread, turn string) {
	t.Helper()
	member, ok := s.Member(thread)
	if !ok {
		t.Fatal("missing member", thread)
	}
	must(t, s.RequestStarted(t.Context(), Identity{Thread: thread, Session: member.Session, Turn: turn}, member.Parent == ""))
}

func deliveries(tree Tree) []Effect {
	var result []Effect
	for _, effect := range tree.Effects {
		if effect.delivered() {
			result = append(result, effect)
		}
	}
	return result
}

func claimPending(t *testing.T, s *Service, ctx context.Context, caller string) (*Effect, error) {
	t.Helper()
	tree, err := s.Snapshot(caller)
	if err != nil {
		return nil, err
	}
	for _, effect := range tree.Actions() {
		if effect.Caller == caller && (effect.State == "pending" || effect.State == "queued") {
			return s.ClaimEffect(ctx, caller, effect.ID)
		}
	}
	return nil, nil
}
