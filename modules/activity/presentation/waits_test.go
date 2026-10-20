package presentation

import (
	"slices"
	"testing"

	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/contracts/scope"
)

func TestWaitScopesClosureAndDetachedPublications(t *testing.T) {
	var waits Waits
	var first, second scope.Store
	in := WaitObservation{Item: observation.Item{ID: "wait", Status: "inProgress"}, Phase: "item/started", Observed: true, Caller: "/root",
		Agents: []WaitAgent{{Thread: "a", Name: "/root/alpha", Responding: true}}}
	start := waits.Observe(&first, in)
	if !start.Retain || len(start.Snapshot) != 1 || start.Value.Body != "Waiting for agent" {
		t.Fatalf("start = %+v", start)
	}
	retained := slices.Clone(start.Snapshot)
	start.Snapshot[0].Name, start.Value.WaitTargets[0].Name = "changed", "changed"
	in.Agents[0].Name = "/root/renamed"
	duplicate := waits.Observe(&first, in)
	if duplicate.Retain || duplicate.Value.WaitTargets[0].Name != "/root/alpha" {
		t.Fatalf("duplicate start changed original publication: %+v", duplicate)
	}
	if got := waits.Observe(&second, in); got.Value.WaitTargets[0].Name != "/root/renamed" {
		t.Fatalf("new turn reused a snapshot: %+v", got)
	}
	in.Item.ID = "second"
	waits.Observe(&first, in)
	in.Item.ID = "done"
	waits.Observe(&first, in)
	in.Phase, in.Item.Status = "item/completed", "completed"
	waits.Observe(&first, in)
	in.Phase = "item/started"
	waits.Observe(&first, in) // A delayed start cannot revive completed presentation.
	ended := waits.EndTurn(&first)
	if len(ended) != 2 || ended[0].ID != "wait" || ended[1].ID != "second" || ended[0].Value.Body != "Wait ended" || ended[0].Value.WaitTargets[0].Name != "/root/alpha" {
		t.Fatalf("ordered turn closure = %+v", ended)
	}
	if waits.HasSnapshot(&first, "wait") || len(waits.EndTurn(&first)) != 0 || len(waits.EndTurn(&second)) != 1 {
		t.Fatal("turn closure retained state or closed another scope")
	}
	in.Item.ID, in.Phase, in.SnapshotFound, in.Snapshot = "wait", "item/completed", true, retained
	in.Agents = nil
	late := waits.Observe(nil, in)
	if late.Value.Body != "Finished waiting" || !slices.Equal(late.Value.WaitTargets, []WaitTarget{{Name: "/root/alpha"}}) || len(waits.EndTurn(nil)) != 0 {
		t.Fatalf("late result revived active state: %+v", late)
	}
}

func TestWaitHistoryNeverInfersTargets(t *testing.T) {
	var waits Waits
	var turn scope.Store
	in := WaitObservation{Item: observation.Item{ID: "wait", Status: "inProgress"}, Phase: "item/started", Caller: "/root",
		Agents: []WaitAgent{{Thread: "today", Name: "/root/today", Responding: true}}}
	if restored := waits.Observe(&turn, in); restored.Retain || len(restored.Value.WaitTargets) != 0 {
		t.Fatalf("history inferred today's targets: %+v", restored)
	}
	in.Observed = true
	if duplicate := waits.Observe(&turn, in); duplicate.Retain || len(duplicate.Value.WaitTargets) != 0 {
		t.Fatalf("late start replaced unknown historical targets: %+v", duplicate)
	}
	in.Phase, in.Item.Status = "item/completed", "completed"
	in.Item.ReceiverThreadIDs = []string{"known"}
	in.Item.AgentsStates = map[string]observation.AgentState{"known": {Status: "running"}, "extra": {Status: "completed"}}
	completed := waits.Observe(&turn, in)
	want := []WaitTarget{{Name: "known", Status: "Still running"}, {Name: "extra", Status: "completed"}}
	if !slices.Equal(completed.Value.WaitTargets, want) {
		t.Fatalf("completion lost explicit evidence: %+v", completed)
	}
}
