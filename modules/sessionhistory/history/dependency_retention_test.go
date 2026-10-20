package history

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/contracts/recordkind"
)

func TestRetentionAbsentLegacyOwnerProtectsSharedPayload(t *testing.T) {
	directory := t.TempDir()
	kinds := retentionFixtureKinds()
	store := retentionFixtureStore(t, directory, kinds...)
	if err := store.Transaction(t.Context(), nil, func(tx *Transaction) error {
		if err := tx.Write("payload-shared", "pending-", []byte("shared bytes")); err != nil {
			return err
		}
		return tx.Write("entry-legacy", "pending-", []byte("payload-shared"))
	}); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-SessionRetention - time.Hour)
	for _, name := range []string{"payload-shared", "entry-legacy"} {
		if err := os.Chtimes(filepath.Join(directory, name), old, old); err != nil {
			t.Fatal(err)
		}
	}
	absent := retentionFixtureStore(t, directory, kinds[1])
	if err := absent.Cleanup(t.Context(), nil, Limits{}, "", nil); err != nil {
		t.Fatal(err)
	}
	requireRetentionFiles(t, directory, true, "payload-shared", "entry-legacy")
	if err := store.Cleanup(t.Context(), nil, Limits{}, "", nil); err != nil {
		t.Fatal(err)
	}
	requireRetentionFiles(t, directory, false, "payload-shared", "entry-legacy")
}

func TestRetentionDependencyOrderSurvivesInterruptedBatch(t *testing.T) {
	for _, catalogued := range []bool{false, true} {
		t.Run(fmt.Sprint("catalogued=", catalogued), func(t *testing.T) {
			interrupted := errors.New("interrupt following retirement batch")
			failNext := func(context.Context, recordkind.Retirement) (*recordkind.RetirementUpdate, error) {
				return nil, interrupted
			}
			kinds := []recordkind.Spec{
				{ID: "entry", Match: func(name string) bool { return strings.HasPrefix(name, "z-entry-") }, Dependencies: func(recordkind.Record) ([]string, error) { return []string{"a-payload"}, nil }},
				{ID: "payload", Match: func(name string) bool { return name == "a-payload" }},
				{ID: "index", Match: func(name string) bool { return name == "index" }, Keep: true, Retirement: func(context.Context, recordkind.Record) (recordkind.RetirementPlanner, error) {
					return func(context.Context, recordkind.Retirement) (*recordkind.RetirementUpdate, error) {
						return &recordkind.RetirementUpdate{Data: []byte("updated"), Next: failNext}, nil
					}, nil
				}},
			}
			store := retentionFixtureStore(t, t.TempDir(), kinds...)
			if err := store.Transaction(t.Context(), nil, func(tx *Transaction) error {
				if err := tx.Write("a-payload", "pending-", []byte("shared bytes")); err != nil {
					return err
				}
				if err := tx.Write("index", "pending-", []byte("original")); err != nil {
					return err
				}
				var names []string
				for i := range 17 {
					name := fmt.Sprintf("z-entry-%02d", i)
					if err := tx.Write(name, "pending-", []byte("references shared bytes")); err != nil {
						return err
					}
					names = append(names, name)
				}
				if catalogued {
					return tx.Retain("old", Limits{}, names...)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if catalogued {
				ageRetentionCatalog(t, store, "old")
			} else {
				old := time.Now().Add(-SessionRetention - time.Hour)
				entries, err := os.ReadDir(store.directory)
				if err != nil {
					t.Fatal(err)
				}
				for _, entry := range entries {
					if err := os.Chtimes(filepath.Join(store.directory, entry.Name()), old, old); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := store.Cleanup(t.Context(), nil, Limits{}, "", nil); !errors.Is(err, interrupted) {
				t.Fatalf("cleanup error: %v", err)
			}
			// The next candidate or batch still has a live referrer after the
			// interruption, even though its payload sorts before every referrer.
			requireRetentionFiles(t, store.directory, true, "z-entry-16", "a-payload")
			requireRetentionFiles(t, store.directory, false, "z-entry-00")
			// A fresh plan has the same owner codec without the injected failure.
			kinds[2].Retirement = nil
			reopened := retentionFixtureStore(t, store.directory, kinds...)
			if err := reopened.Cleanup(t.Context(), nil, Limits{}, "", nil); err != nil {
				t.Fatal(err)
			}
			requireRetentionFiles(t, store.directory, false, "z-entry-16", "a-payload")
		})
	}
}
