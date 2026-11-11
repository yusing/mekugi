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
		_, err := s.RegisterWait(ctx, root, WaitAll, nil, nil, now)
		must(t, err)
	}
	worker := identity("worker", "wt")
	_, err := s.RegisterCompaction(ctx, worker, WaitAll, nil, nil, now, false, "")
	must(t, err)
	_, err = s.RegisterCompaction(ctx, identity("nested", "nt"), "", nil, nil, now, true, "")
	must(t, err)
	if _, err := s.RegisterWait(ctx, identity("second", "st"), WaitAny, nil, nil, now); err == nil {
		t.Fatal("a leaf accepted a descendant wait")
	}
	seedFeatureUse(t, s, "second", FeatureUse{SideQuestions: 1, CompletionChanges: 1})
	must(t, s.HostCompleted(ctx, "second", "st", Completed, HostFailure{}))
	if member, _ := s.Member("second"); member.Status != Completed {
		t.Fatalf("second did not settle: %+v", member.Status)
	}
	seedFeatureUse(t, s, "nested", FeatureUse{CompactionPreloads: 1})
	seedFeatureUse(t, s, "root", FeatureUse{SpawnPreloads: 1})

	self, descendants, threads := featureUse(t, s, "root")
	if self != (FeatureUse{Spawns: 2, Waits: 1, AllWaits: 1, SpawnPreloads: 1}) || len(threads) != 3 {
		t.Fatalf("root self = %+v, threads = %v", self, threads)
	}
	if descendants != (FeatureUse{SelfCompactions: 2, Spawns: 1, Waits: 1, AllWaits: 1, SideQuestions: 1, CompletionChanges: 1, CompactionPreloads: 1}) {
		t.Fatalf("root descendants = %+v", descendants)
	}
	self, descendants, threads = featureUse(t, s, "worker")
	if self != (FeatureUse{SelfCompactions: 1, Spawns: 1, Waits: 1, AllWaits: 1}) || descendants != (FeatureUse{SelfCompactions: 1, CompactionPreloads: 1}) || len(threads) != 1 || threads[0] != "nested" {
		t.Fatalf("worker = %+v / %+v / %v", self, descendants, threads)
	}
	if snapshot := s.FeatureUse(t.Context(), "unknown"); snapshot.Err == nil || snapshot.Threads != nil {
		t.Fatal("an unknown thread acquired feature use")
	}
}

func TestCompactionFeatureCounts(t *testing.T) {
	for _, selection := range []string{"", `{"sources":[]}`, `{"sources":["@AGENTS.md"]}`, `{"sources":["@task.json#L2-L4"]}`} {
		t.Run(selection, func(t *testing.T) {
			s, _ := fixture(t)
			id := identity("root", "rt")
			for range 2 {
				result, err := s.RegisterCompaction(t.Context(), id, "", nil, nil, time.Now(), true, selection)
				must(t, err)
				if result.Compaction.Reload != selection {
					t.Fatal("registration lost selection")
				}
			}
			// Another call in the same cell cannot replace the checkpoint selection.
			result, err := s.RegisterCompaction(t.Context(), id, "", nil, nil, time.Now(), true, `{"sources":["@other"]}`)
			must(t, err)
			if result.Compaction.Reload != selection {
				t.Fatal("repeated registration replaced selection")
			}
			s.RecordFeature(t.Context(), id.Thread, "reset", FeatureEvent{Kind: FeatureResetCompleted})
			s.RecordFeature(t.Context(), id.Thread, "reset", FeatureEvent{Kind: FeatureResetCompleted})
			expected := uint64(0)
			if strings.Contains(selection, "task.json") {
				expected = 1
			}
			if use, _, _ := featureUse(t, s, id.Thread); use.SelfCompactions != 1 || use.Compactions != 1 || use.CompactionPreloads != expected {
				t.Fatal(use)
			}
		})
	}
}

func TestSelfCompactionOutcomeFeatures(t *testing.T) {
	for _, outcome := range []string{"completed", "reconciled", "failed"} {
		t.Run(outcome, func(t *testing.T) {
			s, _ := fixture(t)
			id := identity("root", "rt")
			_, err := s.RegisterCompaction(t.Context(), id, "", nil, nil, time.Now(), true, "")
			must(t, err)
			must(t, s.Execution(t.Context(), id.Thread, id.Turn, id.Item, "", true))
			want := FeatureUse{SelfCompactions: 1}
			_, err = s.ClaimLocalStop(t.Context(), id.Thread, id.Turn, id.Item)
			must(t, err)
			must(t, s.HostCompleted(t.Context(), id.Thread, id.Turn, Completed, HostFailure{}))
			effect, err := s.ClaimEffect(t.Context(), id.Thread, id.Call+":compact")
			must(t, err)
			receipt := Receipt{}
			if outcome == "failed" {
				receipt.Error = "reset failed"
				want.SelfCompactionFailures = 1
			} else {
				want.SelfCompactionsCompleted = 1
				if outcome == "reconciled" {
					must(t, s.EffectAcknowledged(t.Context(), id.Thread, effect.ID, Receipt{Uncertain: true, Error: "lost acknowledgement"}))
					if use, _, _ := featureUse(t, s, id.Thread); use.SelfCompactionFailures != 0 || use.SelfCompactionsCompleted != 0 {
						t.Fatal("uncertain reset settled", use)
					}
					receipt.Reconciled = true
				}
			}
			for range 2 {
				must(t, s.EffectAcknowledged(t.Context(), id.Thread, effect.ID, receipt))
			}
			if use, _, _ := featureUse(t, s, id.Thread); use != want {
				t.Fatalf("use=%+v want=%+v", use, want)
			}
			// Manual/automatic reset observations remain independent of requests.
			s.RecordFeature(t.Context(), id.Thread, "native-reset", FeatureEvent{Kind: FeatureResetCompleted})
			s.RecordFeature(t.Context(), id.Thread, "native-reset", FeatureEvent{Kind: FeatureResetCompleted})
			want.Compactions = 1
			if use, _, _ := featureUse(t, s, id.Thread); use != want {
				t.Fatalf("native reset changed self outcomes: %+v", use)
			}
		})
	}
}
