package control

import (
	"strings"
	"testing"
	"time"
)

func TestFeatureUseSeparatesSelfFromAllDescendants(t *testing.T) {
	s, _ := fixture(t)
	ctx, now := t.Context(), time.Unix(100, 0)
	child(t, s, "root", "worker", "/root/worker", "wt")
	child(t, s, "worker", "nested", "/root/worker/nested", "nt")
	child(t, s, "root", "second", "/root/second", "st")

	root := identity("root", "rt")
	for range 2 { // Repeating the call in one enclosing execution reuses its wait.
		_, err := s.RegisterWait(ctx, root, nil, true, WaitAll, nil, now)
		must(t, err)
	}
	worker := identity("worker", "wt")
	_, err := s.RegisterCompaction(ctx, worker, nil, true, WaitAll, nil, now, false, "")
	must(t, err)
	_, err = s.RegisterCompaction(ctx, identity("nested", "nt"), nil, false, "", nil, now, true, "")
	must(t, err)
	if _, err := s.RegisterWait(ctx, identity("second", "st"), nil, true, WaitAny, nil, now); err == nil {
		t.Fatal("a leaf accepted a descendant wait")
	}
	s.ObserveFeature("second", FeatureSideQuestion)
	s.ObserveFeature("second", FeatureCompletionChanges)
	must(t, s.HostCompleted(ctx, "second", "st", Completed, HostFailure{}))
	if member, _ := s.Member("second"); member.Status != Completed {
		t.Fatalf("second did not settle: %+v", member.Status)
	}
	s.ObserveFeature("nested", FeatureCompactionPreload)
	s.ObserveFeature("root", FeatureSpawnPreload)

	self, descendants, threads := s.FeatureUse("root")
	if self != (FeatureUse{Spawns: 2, Waits: 1, AllWaits: 1, SpawnPreloads: 1}) || len(threads) != 3 {
		t.Fatalf("root self = %+v, threads = %v", self, threads)
	}
	if descendants != (FeatureUse{SelfCompactions: 2, Spawns: 1, Waits: 1, AllWaits: 1, SideQuestions: 1, CompletionChanges: 1, CompactionPreloads: 1}) {
		t.Fatalf("root descendants = %+v", descendants)
	}
	self, descendants, threads = s.FeatureUse("worker")
	if self != (FeatureUse{SelfCompactions: 1, Spawns: 1, Waits: 1, AllWaits: 1}) || descendants != (FeatureUse{SelfCompactions: 1, CompactionPreloads: 1}) || len(threads) != 1 || threads[0] != "nested" {
		t.Fatalf("worker = %+v / %+v / %v", self, descendants, threads)
	}
	if self, descendants, threads := s.FeatureUse("unknown"); self != (FeatureUse{}) || descendants != (FeatureUse{}) || threads != nil {
		t.Fatal("an unknown thread acquired feature use")
	}
}

func TestCompactionFeatureCounts(t *testing.T) {
	for _, selection := range []string{"", `{"sources":[]}`, `{"sources":[{"path":"AGENTS.md"}]}`, `{"sources":[{"path":"task.json","ranges":[[2,4]]}]}`} {
		t.Run(selection, func(t *testing.T) {
			s, _ := fixture(t)
			id := identity("root", "rt")
			for range 2 {
				result, err := s.RegisterCompaction(t.Context(), id, nil, false, "", nil, time.Now(), true, selection)
				must(t, err)
				if result.Compaction.Reload != selection {
					t.Fatal("registration lost selection")
				}
			}
			// Another call in the same cell cannot replace the checkpoint selection.
			result, err := s.RegisterCompaction(t.Context(), id, nil, false, "", nil, time.Now(), true, `{"sources":[{"path":"other"}]}`)
			must(t, err)
			if result.Compaction.Reload != selection {
				t.Fatal("repeated registration replaced selection")
			}
			s.ObserveCompaction(id.Thread, "reset")
			s.ObserveCompaction(id.Thread, "reset")
			expected := uint64(0)
			if strings.Contains(selection, "task.json") {
				expected = 1
			}
			if use, _, _ := s.FeatureUse(id.Thread); use.SelfCompactions != 1 || use.Compactions != 1 || use.CompactionPreloads != expected {
				t.Fatal(use)
			}
		})
	}
}
