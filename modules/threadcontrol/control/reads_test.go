package control

import (
	"bytes"
	json "encoding/json/v2"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestSelectedReadsDetachPayloadsAndSettings(t *testing.T) {
	s, _ := fixture(t)
	caller := identity("root", "rt")
	caller.Call = "spawn-read"
	reserved, err := s.ReserveSpawn(t.Context(), caller, SpawnRequest{Name: "reader", Message: "work", Arguments: []byte(`{"name":"reader"}`), Settings: Settings{Workspace: t.TempDir(), RuntimeRoots: []string{"/admitted"}}, Limits: Limits{MinWaitMS: 5, MaxWaitMS: 100}})
	must(t, err)
	effect, err := s.Effect("root", reserved.ID)
	must(t, err)
	effect.Child.Settings.RuntimeRoots[0] = "changed"
	effect.Child.Arguments[0] = 'x'
	again, err := s.Effect("root", reserved.ID)
	must(t, err)
	if again.Child.Settings.RuntimeRoots[0] != "/admitted" || again.Child.Arguments[0] != '{' {
		t.Fatal("selected effect aliases checkpoint")
	}
	if missing, err := s.Effect("root", "missing"); err != nil || missing != nil {
		t.Fatalf("missing effect: %+v %v", missing, err)
	}
	child(t, s, "root", "worker", "/root/worker", "wt")
	action, err := s.Action(Effect{Caller: "root", Target: "worker", Kind: "create", Phase: "initial"})
	must(t, err)
	if action.Sender != "/root" || action.Recipient != "/root/worker" || action.Settings == nil {
		t.Fatalf("input facts: %+v", action)
	}
	action.Settings.RuntimeRoots[0] = "changed"
	member, ok := s.Member("worker")
	if !ok || member.Settings.RuntimeRoots[0] != "/admitted" {
		t.Fatal("action settings alias checkpoint")
	}
}

func TestSelectedAgentWaitViewDetachesNestedValues(t *testing.T) {
	s, _ := fixture(t)
	child(t, s, "root", "worker", "/root/worker", "wt")
	_, err := s.RegisterWait(t.Context(), identity("root", "rt"), []string{"worker"}, false, new(int64(50)), time.Now())
	must(t, err)
	tree, err := s.Snapshot("root")
	must(t, err)
	listed, err := s.Agents("root", "/root")
	must(t, err)
	rows := listed["agents"].([]map[string]any)
	if len(rows) != 2 || rows[0]["agent_name"] != "/root" || rows[1]["agent_name"] != "/root/worker" {
		t.Fatalf("unexpected agents: %+v", rows)
	}
	wait := rows[0]["wait"].(map[string]any)
	encode := func(value any) []byte {
		t.Helper()
		data, err := json.Marshal(value, json.Deterministic(true))
		must(t, err)
		return data
	}
	want := encode(tree.Members["root"].Wait.View(&tree))
	if !bytes.Equal(encode(wait), want) {
		t.Fatal("selected wait changed display facts")
	}
	wait["targets"].([]string)[0] = "changed"
	*wait["requested_ms"].(*int64) = 1
	*wait["deadline"].(*time.Time) = time.Time{}
	listed, err = s.Agents("root", "/root")
	must(t, err)
	if !bytes.Equal(encode(listed["agents"].([]map[string]any)[0]["wait"]), want) {
		t.Fatal("agent wait aliases retained facts")
	}
	filtered, err := s.Agents("root", "/root/work")
	must(t, err)
	if len(filtered["agents"].([]map[string]any)) != 0 {
		t.Fatal("prefix matched a partial task component")
	}
}

func BenchmarkSelectedEffectRead(b *testing.B) {
	for _, count := range []int{100, 1000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			s := New()
			owner := new(treeOwner)
			tree := &Tree{Version: checkpointVersion, Root: "root", Members: map[string]*Member{"root": {Thread: "root", Task: "/root"}}}
			for i := range count {
				tree.Effects = append(tree.Effects, Effect{ID: fmt.Sprint(i), Caller: "root", Target: "root", Kind: "message", State: "confirmed", Message: strings.Repeat("x", 4096)})
			}
			owner.state.Store(tree)
			s.trees["root"] = owner
			for _, full := range []bool{false, true} {
				name := "selected"
				if full {
					name = "snapshot"
				}
				b.Run(name, func(b *testing.B) {
					b.ReportAllocs()
					for b.Loop() {
						if full {
							if _, err := s.Snapshot("root"); err != nil {
								b.Fatal(err)
							}
						} else {
							if value, err := s.Effect("root", "0"); err != nil || value == nil {
								b.Fatal("selected effect missing", err)
							}
						}
					}
				})
			}
		})
	}
}
