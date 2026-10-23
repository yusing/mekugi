package history

import (
	"fmt"
	"maps"
	"strings"
	"testing"

	"github.com/yusing/mekugi/contracts/recordkind"
)

func TestCleanupClassificationGrowsLinearly(t *testing.T) {
	var previous int
	for _, records := range []int{64, 128} {
		t.Run(fmt.Sprint(records), func(t *testing.T) {
			calls := 0
			store := retentionFixtureStore(t, t.TempDir(), recordkind.Spec{
				ID: "entry", Match: func(name string) bool {
					calls++
					return strings.HasPrefix(name, "entry-")
				},
			})
			if err := store.Transaction(t.Context(), nil, func(tx *Transaction) error {
				for i := range records {
					name := fmt.Sprintf("entry-%04d", i)
					if err := tx.Write(name, "entry-pending-", []byte("x")); err != nil {
						return err
					}
					// Mix recent orphan records with recent catalog candidates.
					if i%2 == 0 {
						if err := tx.Retain(fmt.Sprint("owner-", i), Limits{}, name); err != nil {
							return err
						}
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			calls = 0
			if err := store.Cleanup(t.Context(), nil, Limits{}, "", nil); err != nil {
				t.Fatal(err)
			}
			t.Logf("records=%d classifier calls=%d", records, calls)
			if previous > 0 && calls > 3*previous {
				t.Fatalf("doubling retained records grew classifier calls from %d to %d", previous, calls)
			}
			previous = calls
			if err := store.Transaction(t.Context(), nil, func(tx *Transaction) error {
				files, err := tx.FileSizes()
				if err == nil && len(files) != records+records/2 {
					t.Fatalf("recent history changed: got %d files", len(files))
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestUsageBudgetSelection(t *testing.T) {
	store := retentionFixtureStore(t, t.TempDir(),
		recordkind.Spec{ID: "entry", Match: func(n string) bool { return strings.HasPrefix(n, "entry-") }, Limit: 70, MaxRecordBytes: 50},
		recordkind.Spec{ID: "separate", Match: func(n string) bool { return strings.HasPrefix(n, "separate-") }, SeparateBudget: true, Limit: 90},
	)
	files := map[string]int64{"entry-a": 40, "entry-b": 20, "separate-a": 80, SessionName("owner"): 10}
	for _, test := range []struct {
		name, target string
		size         int64
		limits       Limits
		used, limit  int64
	}{
		{"aggregate excludes separate", "entry-new", 10, Limits{Total: 100}, 80, 100},
		{"kind limit", "entry-new", 20, Limits{Total: 100}, 80, 70},
		{"replacement", "entry-a", 30, Limits{Total: 100}, 60, 100},
		{"kind override", "entry-new", 20, Limits{Total: 100, Kinds: map[string]int64{"entry": 90}}, 90, 100},
		{"zero disables shared kind limit", "entry-new", 20, Limits{Kinds: map[string]int64{"entry": 0}}, 90, DefaultStorageBytes},
		{"record limit precedes budgets", "entry-a", 51, Limits{Total: 1}, 51, 50},
		{"separate excludes aggregate", "separate-new", 11, Limits{Total: 1}, 91, 90},
		{"separate override", "separate-a", 70, Limits{Kinds: map[string]int64{"separate": 75}}, 70, 75},
		{"separate zero override", "separate-new", 1, Limits{Kinds: map[string]int64{"separate": 0}}, 81, 0},
		{"catalog counts in aggregate", SessionName("owner"), 20, Limits{Total: 100}, 80, 100},
	} {
		t.Run(test.name, func(t *testing.T) {
			used, limit := store.Usage(test.limits, files, test.target, test.size)
			if used != test.used || limit != test.limit {
				t.Fatalf("Usage=(%d,%d), want (%d,%d)", used, limit, test.used, test.limit)
			}
		})
	}
}

func TestPruneUsageTracksCommittedEffects(t *testing.T) {
	for _, target := range []string{"", "entry-new", "separate-new", "index-a"} {
		t.Run("target="+target, func(t *testing.T) {
			m, _, _ := retirementPlanFixture(t, recordkind.Spec{
				ID: "separate", Match: func(n string) bool { return strings.HasPrefix(n, "separate-") }, SeparateBudget: true, Limit: 20,
			})
			if err := m.Transaction(t.Context(), nil, func(tx *Transaction) error {
				if err := tx.Write("separate-old", "separate-pending-", []byte("separate bytes")); err != nil {
					return err
				}
				if err := tx.Retain("owner-0", Limits{}, "separate-old"); err != nil {
					return err
				}
				if target != "" {
					return tx.requestStoragePrune(storagePressureRequest{Name: target, Growth: 7})
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			plan, err := m.planStoragePrune(t.Context())
			if err != nil || plan == nil || len(plan.snapshot.sessions) != 1 {
				t.Fatalf("plan=%v err=%v", plan, err)
			}
			candidate := &plan.snapshot.sessions[0]
			for batch := 0; ; batch++ {
				// Read through another Store so the oracle cannot reuse the plan's map.
				var files map[string]int64
				if err := m.service.Attach(m.directory).Transaction(t.Context(), nil, func(tx *Transaction) error {
					var err error
					files, err = tx.FileSizes()
					return err
				}); err != nil {
					t.Fatal(err)
				}
				if !maps.Equal(files, plan.snapshot.files) {
					t.Fatalf("batch %d: planned sizes differ from retained files", batch)
				}
				want := m.storageUsage(files, target)
				if plan.usage.total != want.total || plan.usage.own != want.own {
					t.Fatalf("batch %d: usage=(%d,%d), recomputed=(%d,%d)", batch, plan.usage.total, plan.usage.own, want.total, want.own)
				}
				for _, limits := range []Limits{{}, {Total: 1}, {Total: 20, Kinds: map[string]int64{"entry": 3, "separate": 0, "index": 1}}} {
					m.limits = limits
					size := files[target]
					if plan.pressure != nil {
						size += plan.pressure.Growth
					}
					used, limit := m.Usage(limits, files, target, size)
					if got := m.pruneRequired(plan); got != (used > limit) {
						t.Fatalf("batch %d: pruneRequired=%v, recomputed usage=%d limit=%d", batch, got, used, limit)
					}
				}
				if len(candidate.files) == 0 {
					if batch != 2 || files["index-a"] != 2 || files["index-z"] != 2 {
						t.Fatalf("retirement did not cross size boundary: batches=%d files=%v", batch, files)
					}
					break
				}
				committed, _, err := m.commitStoragePrune(t.Context(), plan, candidate)
				if err != nil || !committed {
					t.Fatalf("batch %d: committed=%v err=%v", batch, committed, err)
				}
			}
		})
	}
}
