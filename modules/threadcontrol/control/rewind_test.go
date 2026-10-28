package control

import (
	"reflect"
	"testing"

	"github.com/yusing/mekugi/contracts/guidance"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

func TestRewindPreservesLiveDescendantsAndDeliveryIdentity(t *testing.T) {
	s, session := fixture(t)
	child(t, s, "root", "worker", "/root/worker", "wt")
	child(t, s, "worker", "nested", "/root/worker/nested", "nt")
	must(t, s.AdmitNamespace(t.Context(), "worker"))
	must(t, s.AdmitNamespace(t.Context(), "nested"))
	must(t, s.update(t.Context(), "root", func(_ *Tree, member *Member) error {
		member.HostRunning, member.Status = false, Completed
		member.Instructions = []guidance.Fragment{{Thread: "root", Item: "role", Kind: "multi_agent.role_instructions", Text: "native role"}}
		return nil
	}))
	caller := identity("nested", "nt")
	caller.Call = "already-dispatched"
	_, err := s.PrepareAction(t.Context(), caller, "agent:main", "message", "in flight")
	must(t, err)
	_, err = s.ClaimEffect(t.Context(), caller.Thread, caller.Call)
	must(t, err)
	caller.Call = "not-dispatched"
	_, err = s.PrepareAction(t.Context(), caller, "agent:main", "message", "queued")
	must(t, err)
	before, err := s.Snapshot("root")
	must(t, err)
	must(t, session.Store.Transaction(t.Context(), session.Writes, func(tx *history.Transaction) error {
		return tx.InitializeScope(history.Scope{Thread: "branch", Namespace: "root", Fork: "root"}, nil)
	}))
	must(t, s.Rewind(t.Context(), "root", ThreadInfo{Thread: "branch", Session: "branch-session", Path: "/branch.jsonl"}))
	after, err := s.Snapshot("branch")
	must(t, err)
	for _, id := range []string{"worker", "nested"} {
		want := *before.Members[id]
		if want.Parent == "root" {
			want.Parent = "branch"
		}
		if !reflect.DeepEqual(*after.Members[id], want) {
			t.Fatalf("rewind changed native descendant %s: before=%+v after=%+v", id, want, after.Members[id])
		}
	}
	if after.Root != "branch" || after.Members["root"].AudienceAdmitted || after.Members["branch"].Instructions[0].Thread != "branch" {
		t.Fatalf("root authority/provenance was not transferred: %+v", after)
	}
	if after.Effect("nested", "already-dispatched").Target != "root" || after.Effect("nested", "not-dispatched").Target != "branch" {
		t.Fatal("rewind changed dispatched delivery or stranded unsent delivery")
	}
	member, err := s.Target(identity("nested", "nt"), "agent:main")
	must(t, err)
	if member.Thread != "branch" {
		t.Fatal("logical root resolves to the old native root")
	}
	if _, err := s.Target(identity("nested", "nt"), "root"); err == nil {
		t.Fatal("previous native root retained targeting authority")
	}
	if s.ObserveAudience("root", func(string, string, string, bool) bool { t.Fatal("historical root observed as active"); return true }) {
		t.Fatal("historical root retained audience authority")
	}
	if !s.ObserveAudience("worker", func(thread, parent, task string, child bool) bool {
		return thread == "worker" && parent == "branch" && task == "/root/worker" && child
	}) {
		t.Fatal("request preparation did not observe canonical rewound ancestry")
	}
	var handles []string
	for _, selected := range []string{"branch", "worker"} {
		namespace, err := session.Store.NamespaceForThread(selected)
		must(t, err)
		must(t, session.Store.Transaction(t.Context(), session.Writes, func(tx *history.Transaction) error {
			ids, err := tx.ReserveHandles(namespace, 1)
			handles = append(handles, ids...)
			return err
		}))
	}
	if len(handles) != 2 || handles[0] == handles[1] {
		t.Fatalf("root and descendant reused a retained handle: %v", handles)
	}
	for _, selected := range []string{"root", "branch", "worker", "nested"} {
		namespace, err := session.Store.NamespaceForThread(selected)
		must(t, err)
		for _, id := range handles {
			owner, err := session.Store.HandleOwner(namespace, id)
			must(t, err)
			if owner != "root" {
				t.Fatalf("%s resolved %s in foreign namespace %s", selected, id, owner)
			}
		}
		members, _, err := ReadFamily(session.Store, selected)
		must(t, err)
		wantRoot := "branch"
		if selected == "root" {
			wantRoot = "root"
		}
		if len(members) != 3 || members[0].Thread != wantRoot || members[1].Parent != wantRoot {
			t.Fatalf("retained family for %s: %+v", selected, members)
		}
	}
	branch, err := session.Store.SelectSession(t.Context(), nil, session.Limits, "branch")
	must(t, err)
	defer branch.Close()
	restarted := New()
	must(t, restarted.Restore(t.Context(), branch))
	if root, members := restarted.Family("worker"); root != "branch" || len(members) != 3 {
		t.Fatalf("cold branch lost retained descendants: %s %+v", root, members)
	}
	must(t, restarted.Restore(t.Context(), session))
	if root, _ := restarted.Family("worker"); root != "root" {
		t.Fatal("original saved branch could not regain root authority")
	}
}
