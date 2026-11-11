package control

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/contracts/recordkind"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

func TestCheckpointDependencies(t *testing.T) {
	dependencies := RecordKinds()[0].Dependencies
	for _, test := range []struct {
		name, data string
		want       []string
		invalid    bool
	}{
		{name: "ordered duplicates", data: `{"Effects":[{"Sources":"source-a","Child":{"Import":"import-a"}},{"Sources":"source-a","Child":{"Import":"import-b"}}]}`, want: []string{"source-a", "import-a", "source-a", "import-b"}},
		{name: "null effects", data: `{"Effects":null}`},
		{name: "null values", data: `{"Effects":[null,{"Sources":null,"Child":null},{"Sources":"","Child":{"Import":null}}]}`},
		{name: "case sensitive", data: `{"effects":[{"Sources":"ignored"}],"Effects":[{"sources":"ignored","child":{"Import":"ignored"},"Child":{"import":"ignored"}}]}`},
		{name: "skip nondependency types", data: `{"Version":"not an integer","Members":[],"Effects":[{"Message":{},"Child":{"Settings":false,"Import":"import-a"}}]}`, want: []string{"import-a"}},
		{name: "effects type", data: `{"Effects":{}}`, invalid: true},
		{name: "effect type", data: `{"Effects":[0]}`, invalid: true},
		{name: "sources type", data: `{"Effects":[{"Sources":1}]}`, invalid: true},
		{name: "child type", data: `{"Effects":[{"Child":[]}]}`, invalid: true},
		{name: "import type", data: `{"Effects":[{"Child":{"Import":false}}]}`, invalid: true},
		{name: "duplicate effects", data: `{"Effects":[],"Effects":[]}`, invalid: true},
		{name: "duplicate sources", data: `{"Effects":[{"Sources":"a","Sources":"b"}]}`, invalid: true},
		{name: "duplicate import", data: `{"Effects":[{"Child":{"Import":"a","Import":"b"}}]}`, invalid: true},
		{name: "duplicate skipped name", data: `{"Members":{"root":{"Session":"a","Session":"b"}}}`, invalid: true},
		{name: "duplicate skipped child name", data: `{"Effects":[{"Child":{"Settings":{"Model":"a","Model":"b"}}}]}`, invalid: true},
		{name: "malformed skipped value", data: `{"Members":{"root":]}`, invalid: true},
		{name: "trailing data", data: `{"Effects":[]} {}`, invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := dependencies(recordkind.Record{Name: recordName("root"), Data: []byte(test.data)})
			if (err != nil) != test.invalid || !test.invalid && !slices.Equal(got, test.want) {
				t.Fatalf("dependencies = %v, %v; want %v, invalid=%v", got, err, test.want, test.invalid)
			}
		})
	}
	// The narrow retention projection must not weaken ordinary restoration.
	for _, data := range []string{
		`{"Version":"not an integer"}`,
		`{"Version":4,"Root":"root","Members":{"root":{"Thread":"root","Session":"session","Task":"/wrong"}}}`,
		`{"Version":4,"Root":"root","Members":{"root":{"Thread":"root","Session":"session","Task":"/root"}},"Effects":[{"Message":{}}]}`,
	} {
		if _, err := decodeTree([]byte(data), "root"); err == nil {
			t.Fatalf("ordinary checkpoint decode accepted invalid schema or identity: %s", data)
		}
	}
}

func TestCheckpointDependenciesRejectUnregisteredRecords(t *testing.T) {
	_, session := fixture(t)
	for _, effect := range []string{`{"Sources":"unregistered-dependency"}`, `{"Child":{"Import":"unregistered-dependency"}}`} {
		err := session.Store.Transaction(t.Context(), session.Writes, func(tx *history.Transaction) error {
			if err := tx.Write("unregistered-dependency", "dependency-pending-", []byte("retained bytes")); err != nil {
				return err
			}
			name := recordName("dependency-test")
			if err := tx.Write(name, "tree-pending-", []byte(`{"Effects":[`+effect+`]}`)); err != nil {
				return err
			}
			return tx.Retain("root", session.Limits, name)
		})
		if err == nil || !strings.Contains(err.Error(), "unregistered retained dependency") {
			t.Fatalf("unregistered dependency was not rejected through history: %v", err)
		}
	}
}

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
	service.BindFeatureUse("root", newFeatureRecorder())
	hostStarted(t, service, "root", "rt")
	return service, session
}

func TestRestoreBeforeAnyRequestLeavesChildrenCold(t *testing.T) {
	s, session := fixture(t)
	child(t, s, "root", "worker", "/root/child-runtime", "wt")
	for _, target := range []string{"child-runtime", "/root/child-runtime", "agent:main/child-runtime"} {
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
	must(t, s.HostCompleted(t.Context(), id.Thread, id.Turn, Completed, HostFailure{}))
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
		r, err := s.RegisterWait(ctx, id, "", nil, nil, now)
		must(t, err)
		if r.State != "waiting" {
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
	must(t, s.AnswerCompleted(ctx, "grandchild", "gt", "answer", "grandchild result"))
	must(t, s.HostCompleted(ctx, "grandchild", "gt", Completed, HostFailure{}))
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
	must(t, s.AnswerCompleted(ctx, "child", "ct2", "answer", "ordinary child result"))
	must(t, s.HostCompleted(ctx, "child", "ct2", Completed, HostFailure{}))
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
	wait, err := s.RegisterWait(t.Context(), caller, "", nil, nil, time.Now())
	must(t, err)
	if wait.State != "waiting" || wait.Event != nil {
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
	wait, err = s.RegisterWait(t.Context(), caller, "", nil, nil, time.Now())
	must(t, err)
	if wait.State != "waiting" {
		t.Fatalf("consumed event was delivered twice: %+v", wait)
	}
	stop(t, s, caller)
	must(t, s.HostCompleted(t.Context(), "a", "at", Completed, HostFailure{}))
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

func TestSequentialWaitsDoNotRepeatCompletedWork(t *testing.T) {
	for _, mode := range []WaitMode{WaitAny, WaitAll} {
		for _, immediate := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/immediate=%t", mode, immediate), func(t *testing.T) {
				s, _ := fixture(t)
				child(t, s, "root", "a", "/root/a", "at")
				child(t, s, "root", "b", "/root/b", "bt")
				caller, now := identity("root", "rt"), time.Now()
				caller.Call, caller.Item = "exec-first", "exec-first-item"
				if !immediate {
					_, err := s.RegisterWait(t.Context(), caller, WaitAny, nil, nil, now)
					must(t, err)
					stop(t, s, caller)
				}
				must(t, s.AnswerCompleted(t.Context(), "a", "at", "answer", "Finished first assignment"))
				must(t, s.HostCompleted(t.Context(), "a", "at", Completed, HostFailure{}))
				deliver(t, s, "a")
				var first *WaitResult
				if immediate {
					result, err := s.RegisterWait(t.Context(), caller, WaitAny, nil, nil, now)
					must(t, err)
					first = &result
				} else {
					var err error
					first, err = claimWake(t, s, t.Context(), "root", now)
					must(t, err)
					must(t, s.EffectAcknowledged(t.Context(), "root", caller.Call, Receipt{Turn: "rt2"}))
					caller.Turn = "rt2"
				}
				if first == nil || first.Event == nil || first.Event.Source != "a" || first.Event.Generation != 1 {
					t.Fatalf("first completion: %+v", first)
				}
				must(t, s.InputConsumed(t.Context(), caller, []string{MessageItemID("a:1:at:final")}))
				caller.Call, caller.Item = "exec-second", "exec-second-item"
				second, err := s.RegisterWait(t.Context(), caller, mode, nil, nil, now)
				must(t, err)
				if second.State != "waiting" || second.Event != nil || !slices.Equal(second.Targets, []string{"/root/b"}) {
					t.Fatalf("settled generation became an exec-prefixed stopped event: %+v", second)
				}
				stop(t, s, caller)
				wake, err := claimWake(t, s, t.Context(), "root", now.Add(time.Hour))
				must(t, err)
				if wake != nil {
					t.Fatalf("old completion woke an indefinite wait: %+v", wake)
				}
				must(t, s.HostCompleted(t.Context(), "b", "bt", Completed, HostFailure{}))
				deliver(t, s, "b")
				wake, err = claimWake(t, s, t.Context(), "root", now.Add(time.Hour))
				must(t, err)
				if wake == nil || wake.State != "completed" {
					t.Fatalf("new completion failed to wake: %+v", wake)
				}
				if mode == WaitAny && (wake.Event == nil || wake.Event.Source != "b") || mode == WaitAll && (len(wake.Events) != 1 || wake.Events[0].Source != "b") {
					t.Fatalf("new wait replayed first completion: %+v", wake)
				}
			})
		}
	}
}

func TestStoppedTargetAfterRestartKeepsResultConsumed(t *testing.T) {
	s, session := fixture(t)
	child(t, s, "root", "a", "/root/a", "at")
	must(t, s.HostCompleted(t.Context(), "a", "at", Completed, HostFailure{}))
	deliver(t, s, "a")
	tree, err := s.Snapshot("root")
	must(t, err)
	final := effectsOf(tree, "final")[0]
	must(t, s.InputConsumed(t.Context(), identity("root", "rt"), []string{MessageItemID(final.ID)}))
	restored := New()
	must(t, restored.Restore(t.Context(), session))
	hostStarted(t, restored, "root", "resumed")
	wait, err := restored.RegisterWait(t.Context(), identity("root", "resumed"), "", nil, nil, time.Now())
	if err == nil || wait.Event != nil {
		t.Fatalf("already-read history became a fresh wait target: %+v %v", wait, err)
	}
	tree, err = restored.Snapshot("root")
	must(t, err)
	if !tree.Effect(final.Caller, final.ID).Consumed {
		t.Fatal("restart made an already-read result deliverable again")
	}
}

func TestWakeDispatchSurvivesTerminalAndColdRestore(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(map[bool]string{false: "cold", true: "terminal_before_ack"}[terminal], func(t *testing.T) {
			s, session := fixture(t)
			child(t, s, "root", "child", "/root/child", "ct")
			_, err := s.RegisterWait(t.Context(), identity("root", "rt"), "", nil, nil, time.Now())
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
				must(t, s.HostCompleted(t.Context(), "child", "ct", Completed, HostFailure{}))
				hostStarted(t, s, "root", "next")
				must(t, s.HostCompleted(t.Context(), "root", "next", Completed, HostFailure{}))
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
	timeout := int64(101)
	if _, err := s.RegisterWait(ctx, caller, "", &timeout, nil, now); err == nil {
		t.Fatal("accepted excessive timeout")
	}
	timeout = 1
	r, err := s.RegisterWait(ctx, caller, "", &timeout, nil, now)
	must(t, err)
	state, err := s.Snapshot("root")
	must(t, err)
	w := state.Members["root"].Wait
	if *w.RequestedMS != 1 || *w.EffectiveMS != 5 || w.Deadline.Sub(now) != 5*time.Millisecond {
		t.Fatal(w)
	}
	must(t, s.Execution(ctx, "root", "rt", caller.Item, "cell:running", false))
	if stopped, err := s.ClaimLocalStop(ctx, "root", "rt", caller.Item); err != nil || stopped != nil {
		t.Fatalf("yielded execution armed stop: %v, %v", stopped, err)
	}
	stop(t, s, caller)
	if wake, err := claimWake(t, s, ctx, "root", now); err != nil || wake != nil {
		t.Fatalf("wait woke before timeout: %v, %v", wake, err)
	}
	wake, err := claimWake(t, s, ctx, "root", now.Add(5*time.Millisecond))
	must(t, err)
	if wake == nil || wake.State != "timeout" || wake.ID != r.ID {
		t.Fatal(wake)
	}

}

func TestPendingWaitRejectsDifferentEnclosingExecution(t *testing.T) {
	s, _ := fixture(t)
	child(t, s, "root", "child", "/root/child", "ct")
	caller := identity("root", "rt")
	wait, err := s.RegisterWait(t.Context(), caller, "", nil, nil, time.Now())
	must(t, err)
	for _, changed := range []Identity{
		{Thread: caller.Thread, Session: caller.Session, Turn: caller.Turn, Call: "another", Item: "another-exec"},
		{Thread: caller.Thread, Session: caller.Session, Turn: "another-turn", Call: caller.Call, Item: caller.Item},
	} {
		if _, err := s.RegisterWait(t.Context(), changed, "", nil, nil, time.Now()); err == nil {
			t.Fatalf("reused a wait that cannot arm for %+v", changed)
		}
	}
	caller.Call = "same-cell"
	repeated, err := s.RegisterWait(t.Context(), caller, "", nil, nil, time.Now())
	must(t, err)
	if repeated.ID != wait.ID {
		t.Fatal("same enclosing execution replaced its wait")
	}
}

func TestWaitSnapshotsAllCurrentDescendants(t *testing.T) {
	s, _ := fixture(t)
	child(t, s, "root", "child", "/root/child", "ct")
	child(t, s, "child", "nested", "/root/child/nested", "nt")
	caller, now := identity("root", "rt"), time.Now()
	wait, err := s.RegisterWait(t.Context(), caller, WaitAny, nil, nil, now)
	must(t, err)
	if !slices.Equal(wait.Targets, []string{"/root/child", "/root/child/nested"}) {
		t.Fatalf("wait omitted descendants: %+v", wait)
	}
	child(t, s, "child", "later", "/root/child/later", "lt")
	stop(t, s, caller)
	must(t, s.HostCompleted(t.Context(), "later", "lt", Completed, HostFailure{}))
	deliver(t, s, "later")
	wake, err := claimWake(t, s, t.Context(), "root", now)
	must(t, err)
	if wake != nil {
		t.Fatal("later descendant joined retained wait")
	}
	must(t, s.HostCompleted(t.Context(), "nested", "nt", Completed, HostFailure{}))
	deliver(t, s, "nested")
	wake, err = claimWake(t, s, t.Context(), "root", now)
	must(t, err)
	if wake == nil || wake.Event == nil || wake.Event.Source != "nested" {
		t.Fatalf("nested descendant did not wake snapshot: %+v", wake)
	}
}

func TestColdResumeDoesNotRearmOrRedispatch(t *testing.T) {
	s, session := fixture(t)
	ctx := t.Context()
	now := time.Unix(100, 0)
	child(t, s, "root", "a", "/root/a", "at")
	id := identity("root", "rt")
	_, err := s.RegisterWait(ctx, id, "", nil, nil, now)
	must(t, err)
	stop(t, s, id)
	must(t, s.AnswerCompleted(ctx, "a", "at", "answer", "result"))
	must(t, s.HostCompleted(ctx, "a", "at", Completed, HostFailure{}))
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
	must(t, s.HostCompleted(ctx, "a", "at", Completed, HostFailure{}))
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
	must(t, s.HostCompleted(t.Context(), "root", "rt", Completed, HostFailure{}))
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
	_, err := s.RegisterWait(t.Context(), identity("root", "rt"), "", nil, nil, time.Now())
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

func TestWaitWakeReasonSurvivesRootTurnObservation(t *testing.T) {
	s, _ := fixture(t)
	child(t, s, "root", "child", "/root/child", "ct")
	id := identity("root", "rt")
	_, err := s.RegisterWait(t.Context(), id, "", nil, nil, time.Unix(100, 0))
	must(t, err)
	stop(t, s, id)
	must(t, s.HostCompleted(t.Context(), "child", "ct", Completed, HostFailure{}))
	deliver(t, s, "child")
	wake, err := claimWake(t, s, t.Context(), "root", time.Unix(101, 0))
	must(t, err)
	if wake == nil {
		t.Fatal("no completion wake")
	}
	must(t, s.EffectAcknowledged(t.Context(), "root", wake.ID, Receipt{Turn: "next"}))
	must(t, s.HostCompleted(t.Context(), "root", "next", Completed, HostFailure{}))
	must(t, s.RequestStarted(t.Context(), identity("root", "later"), true))
	tree, err := s.Snapshot("root")
	must(t, err)
	wait := tree.Members["root"].Wait
	if wait.State != "completed" || tree.waitResult(wait).Event == nil || tree.waitResult(wait).Event.Task != "/root/child" {
		t.Fatalf("turn observation discarded wake reason: %+v", wait)
	}
}

func TestStoppedTargetsSettleWaits(t *testing.T) {
	for _, during := range []bool{false, true} {
		for _, status := range []Status{Paused, Completed, Failed, Interrupted} {
			t.Run(fmt.Sprintf("%s/during=%t", status, during), func(t *testing.T) {
				s, _ := fixture(t)
				child(t, s, "root", "running", "/root/running", "running-turn")
				child(t, s, "root", "stopped", "/root/stopped", "stopped-turn")
				id, now := identity("root", "rt"), time.Unix(100, 0)
				settle := func() {
					if status == Paused {
						must(t, s.Pause(t.Context(), "stopped"))
						return
					}
					must(t, s.HostCompleted(t.Context(), "stopped", "stopped-turn", status, HostFailure{}))
					deliver(t, s, "stopped")
					// A selected target must settle even when its result is read
					// during the wait. Earlier read history is not a new target.
					must(t, s.InputConsumed(t.Context(), id, []string{MessageItemID("stopped:1:stopped-turn:final")}))
				}
				if !during {
					settle()
				}
				result, err := s.RegisterWait(t.Context(), id, "", new(int64(100)), nil, now)
				must(t, err)
				if !during && status != Paused {
					if result.State != "waiting" || !slices.Equal(result.Targets, []string{"/root/running"}) {
						t.Fatalf("already-read history selected: %+v", result)
					}
					return
				}
				if during {
					stop(t, s, id)
					settle()
					wake, err := claimWake(t, s, t.Context(), "root", now.Add(time.Millisecond))
					must(t, err)
					if wake == nil {
						t.Fatal("stopped selected target left wait pending")
					}
					result = *wake
					must(t, s.EffectAcknowledged(t.Context(), "root", id.Call, Receipt{Turn: "continued"}))
				}
				if result.State != "completed" || result.Event == nil || result.Event.Task != "/root/stopped" || result.Event.Status != status {
					t.Fatalf("wrong stopped-target disposition: %+v", result)
				}
				if got := s.WaitPublication("root"); got.Event == nil || got.Event.Status != status {
					t.Fatalf("lost visible reason: %+v", got)
				}
			})
		}
	}
}

func TestWaitSelectionKeepsUnreadResults(t *testing.T) {
	for _, mode := range []WaitMode{WaitAny, WaitAll} {
		for _, delivered := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/delivered=%t", mode, delivered), func(t *testing.T) {
				s, _ := fixture(t)
				child(t, s, "root", "old", "/root/old", "ot")
				child(t, s, "root", "unread", "/root/unread", "ut")
				child(t, s, "root", "working", "/root/working", "wt")
				caller := identity("root", "rt")
				must(t, s.HostCompleted(t.Context(), "old", "ot", Completed, HostFailure{}))
				deliver(t, s, "old")
				must(t, s.InputConsumed(t.Context(), caller, []string{MessageItemID("old:1:ot:final")}))
				must(t, s.HostCompleted(t.Context(), "unread", "ut", Failed, HostFailure{}))
				if delivered {
					deliver(t, s, "unread")
				}
				wait, err := s.RegisterWait(t.Context(), caller, mode, nil, nil, time.Now())
				must(t, err)
				if !slices.Equal(wait.Targets, []string{"/root/unread", "/root/working"}) {
					t.Fatalf("lost unread result or included read history: %+v", wait)
				}
				if mode == WaitAny && delivered {
					if wait.State != "completed" || wait.Event == nil || wait.Event.Source != "unread" {
						t.Fatalf("unread failure did not wake: %+v", wait)
					}
				} else if wait.State != "waiting" {
					t.Fatalf("wait lost selection authority: %+v", wait)
				}
			})
		}
	}
}

func TestWaitMessageWithoutUnsettledTargets(t *testing.T) {
	for _, mode := range []WaitMode{WaitAny, WaitAll} {
		t.Run(string(mode), func(t *testing.T) {
			s, _ := fixture(t)
			caller := identity("root", "rt")
			child(t, s, "root", "old", "/root/old", "ot")
			must(t, s.HostCompleted(t.Context(), "old", "ot", Completed, HostFailure{}))
			deliver(t, s, "old")
			must(t, s.InputConsumed(t.Context(), caller, []string{MessageItemID("old:1:ot:final")}))
			effect, err := s.ReceiveMessage(t.Context(), Origin{Session: "remote", Thread: "sender", Task: "/root", Status: Completed}, "root", "message", "independent notification")
			must(t, err)
			wait, err := s.RegisterWait(t.Context(), caller, mode, nil, nil, time.Now())
			must(t, err)
			if wait.State != "waiting" || len(wait.Targets) != 0 {
				t.Fatalf("pending message invented targets or an outcome: %+v", wait)
			}
			stop(t, s, caller)
			settle(t, s, "root", effect.ID, Receipt{})
			wake, err := claimWake(t, s, t.Context(), "root", time.Now())
			must(t, err)
			if wake == nil || wake.Event == nil || wake.Event.Source != "sender" || wake.Event.Message != "independent notification" || len(wake.Targets) != 0 {
				t.Fatalf("independent notification was lost: %+v", wake)
			}
		})
	}
}

func TestWaitAllSettlesSelectedOutcomesTogether(t *testing.T) {
	for _, immediate := range []bool{false, true} {
		t.Run(fmt.Sprint(immediate), func(t *testing.T) {
			s, session := fixture(t)
			child(t, s, "root", "a", "/root/a", "at")
			child(t, s, "root", "b", "/root/b", "bt")
			caller, now := identity("root", "rt"), time.Now()
			if !immediate {
				wait, err := s.RegisterWait(t.Context(), caller, WaitAll, nil, nil, now)
				must(t, err)
				if wait.State != "waiting" || wait.Mode != WaitAll {
					t.Fatalf("registration: %+v", wait)
				}
				stop(t, s, caller)
			}
			must(t, s.HostCompleted(t.Context(), "a", "at", Failed, HostFailure{}))
			deliver(t, s, "a")
			if !immediate {
				wake, err := claimWake(t, s, t.Context(), "root", now)
				must(t, err)
				if wake != nil {
					t.Fatalf("first failure woke all-wait: %+v", wake)
				}
			}
			must(t, s.HostCompleted(t.Context(), "b", "bt", Completed, HostFailure{}))
			if !immediate {
				wake, err := claimWake(t, s, t.Context(), "root", now)
				must(t, err)
				if wake != nil {
					t.Fatal("pending final delivery woke all-wait")
				}
			}
			deliver(t, s, "b")
			var result *WaitResult
			if immediate {
				wait, err := s.RegisterWait(t.Context(), caller, WaitAll, nil, nil, now)
				must(t, err)
				result = &wait
			} else {
				var err error
				result, err = claimWake(t, s, t.Context(), "root", now)
				must(t, err)
			}
			if result == nil || result.State != "completed" || result.Event != nil || len(result.Events) != 2 || result.Events[0].Status != Failed || result.Events[1].Status != Completed {
				t.Fatalf("combined outcomes: %+v", result)
			}
			// A restart preserves the exact retained aggregate and mode without rearming.
			root, _ := s.Member("root")
			restored := New()
			must(t, restored.AdmitRoot(t.Context(), session, root))
			publication := restored.WaitPublication("root")
			if publication.Mode != WaitAll || !slices.Equal(publication.Events, result.Events) {
				t.Fatalf("restored aggregate: %+v", publication)
			}
			tree, err := s.Snapshot("root")
			must(t, err)
			for _, effect := range effectsOf(tree, "final") {
				if effect.ClaimedBy != caller.Call {
					t.Fatalf("unclaimed result: %+v", effect)
				}
			}
		})
	}
}

func TestWaitAllMessageTimeoutAndStoppedTargets(t *testing.T) {
	for _, reason := range []string{"message", "timeout", "stopped"} {
		t.Run(reason, func(t *testing.T) {
			s, _ := fixture(t)
			child(t, s, "root", "a", "/root/a", "at")
			child(t, s, "root", "b", "/root/b", "bt")
			caller, now := identity("root", "rt"), time.Now()
			must(t, s.HostCompleted(t.Context(), "a", "at", Completed, HostFailure{}))
			deliver(t, s, "a")
			must(t, s.InputConsumed(t.Context(), caller, []string{MessageItemID("a:1:at:final")}))
			wait, err := s.RegisterWait(t.Context(), caller, WaitAll, new(int64(10)), nil, now)
			must(t, err)
			if wait.State != "waiting" {
				t.Fatalf("already-read outcome woke all-wait: %+v", wait)
			}
			stop(t, s, caller)
			switch reason {
			case "message":
				message(t, s, identity("b", "bt"), "progress")
			case "timeout":
				now = now.Add(time.Second)
			case "stopped":
				must(t, s.Pause(t.Context(), "b"))
			}
			wake, err := claimWake(t, s, t.Context(), "root", now)
			must(t, err)
			if wake == nil {
				t.Fatal("missing wake")
			}
			if reason == "message" && (wake.Event == nil || wake.Event.Kind != "message" || len(wake.Events) != 0) {
				t.Fatalf("message wake: %+v", wake)
			}
			if reason == "timeout" && (wake.State != "timeout" || len(wake.Events) != 0) {
				t.Fatalf("timeout wake: %+v", wake)
			}
			if reason == "stopped" && (len(wake.Events) != 1 || wake.Events[0].Status != Paused) {
				t.Fatalf("stopped outcomes: %+v", wake)
			}
		})
	}
}

func TestProviderCapacityBackoff(t *testing.T) {
	service, _ := fixture(t)
	turn := "rt"
	for i, interval := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second} {
		before := time.Now()
		stop, err := service.WaitForCapacity(t.Context(), "root", turn, nil)
		must(t, err)
		member, _ := service.Member("root")
		deadline := *member.Wait.Deadline
		if deadline.Before(before.Add(interval)) || deadline.After(time.Now().Add(interval)) || time.Duration(member.CapacityRetrySeconds)*time.Second != interval {
			t.Fatalf("retry %d: interval=%d deadline=%s", i, member.CapacityRetrySeconds, deadline)
		}
		publication := service.WaitPublication("root")
		if !publication.Capacity || publication.StatusText() == (&WaitResult{UsageLimit: true, Timed: true}).StatusText() {
			t.Fatalf("capacity wait presented as quota: %+v", publication)
		}
		repeated, err := service.WaitForCapacity(t.Context(), "root", turn, nil)
		must(t, err)
		member, _ = service.Member("root")
		if *repeated != *stop || !member.Wait.Deadline.Equal(deadline) {
			t.Fatal("duplicate rejection changed the wait")
		}
		must(t, service.HostCompleted(t.Context(), "root", turn, Completed, HostFailure{}))
		wake, err := service.PrepareWake(t.Context(), "root", deadline.Add(-time.Nanosecond))
		must(t, err)
		if wake != nil {
			t.Fatal("capacity resumed before deadline")
		}
		wake, err = service.PrepareWake(t.Context(), "root", deadline)
		must(t, err)
		if wake == nil {
			t.Fatal("capacity did not resume at deadline")
		}
		_, err = service.ClaimEffect(t.Context(), "root", wake.ID)
		must(t, err)
		turn = fmt.Sprint("retry-", i)
		hostStarted(t, service, "root", turn)
		must(t, service.EffectAcknowledged(t.Context(), "root", wake.ID, Receipt{Turn: turn}))
	}
	// A successful inference resets fallback within the same work generation.
	must(t, service.ProviderRecovered(t.Context(), "root", "stale-turn"))
	member, _ := service.Member("root")
	if member.CapacityRetrySeconds != 30 {
		t.Fatal("stale recovery reset active work")
	}
	must(t, service.ProviderRecovered(t.Context(), "root", turn))
	_, err := service.WaitForCapacity(t.Context(), "root", turn, nil)
	must(t, err)
	member, _ = service.Member("root")
	if member.CapacityRetrySeconds != 1 {
		t.Fatal("recovered work did not restart at one second")
	}
	_, err = service.HumanInterrupt(t.Context(), "root", member.Stop.Wait)
	must(t, err)
	must(t, service.HostCompleted(t.Context(), "root", turn, Interrupted, HostFailure{}))
	wake, err := service.PrepareWake(t.Context(), "root", time.Now().Add(time.Hour))
	must(t, err)
	if wake != nil {
		t.Fatal("interrupted capacity wait resumed")
	}
	hostStarted(t, service, "root", "new-work")
	providerTime := time.Now().Add(90 * time.Second)
	_, err = service.WaitForCapacity(t.Context(), "root", "new-work", &providerTime)
	must(t, err)
	member, _ = service.Member("root")
	if !member.Wait.Deadline.Equal(providerTime) {
		t.Fatal("provider timing was clamped to fallback bounds")
	}
}
