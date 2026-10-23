package history

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/contracts/recordkind"
)

func TestAdmissionWarmReplacementCostIgnoresCorpusSize(t *testing.T) {
	for _, records := range []int{16, 128} {
		t.Run(fmt.Sprint(records), func(t *testing.T) {
			calls := 0
			store := retentionFixtureStore(t, t.TempDir(), recordkind.Spec{ID: "entry", Match: func(n string) bool {
				calls++
				return strings.HasPrefix(n, "entry-")
			}})
			if err := store.Transaction(t.Context(), nil, func(tx *Transaction) error {
				for i := range records {
					if err := tx.Write(fmt.Sprint("entry-", i), "entry-pending-", []byte("x")); err != nil {
						return err
					}
				}
				_, err := tx.FileSizes()
				return err
			}); err != nil {
				t.Fatal(err)
			}
			calls = 0
			for size := 1; size <= 16; size++ {
				if err := store.Transaction(t.Context(), nil, func(tx *Transaction) error {
					if err := tx.Admit(Limits{}, "entry-0", int64(size)); err != nil {
						return err
					}
					return tx.Write("entry-0", "entry-pending-", []byte(strings.Repeat("x", size)))
				}); err != nil {
					t.Fatal(err)
				}
			}
			if calls > 16*4 {
				t.Fatalf("16 warmed admission/replacements classified %d names in %d-record corpus", calls, records)
			}
		})
	}
}

func TestAdmissionCacheMatchesFreshUsageAcrossPublications(t *testing.T) {
	store := retentionFixtureStore(t, t.TempDir(),
		recordkind.Spec{ID: "entry", Match: func(n string) bool { return strings.HasPrefix(n, "entry-") }, Limit: 20, MaxRecordBytes: 30},
		recordkind.Spec{ID: "separate", Match: func(n string) bool { return strings.HasPrefix(n, "separate-") }, SeparateBudget: true, Limit: 15},
	)
	write := func(owner *Store, name, data string) {
		t.Helper()
		if err := owner.Transaction(t.Context(), nil, func(tx *Transaction) error {
			return tx.Write(name, "record-pending-", []byte(data))
		}); err != nil {
			t.Fatal(err)
		}
	}
	check := func(stage string) {
		t.Helper()
		var fresh map[string]int64
		if err := store.service.Attach(store.directory).Transaction(t.Context(), nil, func(tx *Transaction) error {
			var err error
			fresh, err = tx.FileSizes()
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if err := store.Transaction(t.Context(), nil, func(tx *Transaction) error {
			for _, name := range []string{"entry-a", "entry-new", "separate-a", SessionName("owner")} {
				for _, size := range []int64{1, 12, 31} {
					for _, limits := range []Limits{{}, {Total: 10}, {Kinds: map[string]int64{"entry": 0, "separate": 5}}} {
						used, limit := store.Usage(limits, fresh, name, size)
						err := tx.Admit(limits, name, size)
						if used <= limit {
							if err != nil {
								t.Fatalf("%s %s size=%d: unexpected rejection: %v", stage, name, size, err)
							}
						} else {
							required := used
							if size > limit {
								required = size
							}
							var capacity *CapacityError
							if !errors.As(err, &capacity) || capacity.Required != required || capacity.Limit != limit {
								t.Fatalf("%s %s size=%d: got %v, want required=%d limit=%d", stage, name, size, err, required, limit)
							}
						}
					}
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	write(store, "entry-a", "1234567890")
	write(store, "entry-b", "12345")
	write(store, "separate-a", "12345678")
	check("initial")
	write(store, "entry-a", "123456789012345678")
	check("local growth")
	write(store, "entry-a", "12")
	write(store, "separate-a", "123")
	check("local shrink")
	write(store.service.Attach(store.directory), "entry-peer", "123456789012345")
	check("peer publication")
	if err := store.Transaction(t.Context(), nil, func(tx *Transaction) error {
		if err := tx.Write("entry-a", "missing/pending-", []byte("unpublished bytes")); err == nil {
			t.Fatal("invalid temporary path published successfully")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	check("failed replacement")
	failure := errors.New("interrupted publication")
	err := store.Transaction(t.Context(), nil, func(tx *Transaction) error {
		if err := tx.AdvanceRevision(); err != nil {
			return err
		}
		if err := os.Remove(filepath.Join(store.directory, "entry-peer")); err != nil {
			return err
		}
		return failure
	})
	if !errors.Is(err, failure) {
		t.Fatalf("removal transaction: %v", err)
	}
	check("removal before failed publication")
}
