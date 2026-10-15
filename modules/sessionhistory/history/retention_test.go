package history

import (
	"bytes"
	json "encoding/json/v2"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/contracts/recordkind"
)

func retentionFixtureStore(t *testing.T, directory string, kinds ...recordkind.Spec) *Store {
	t.Helper()
	service, err := NewService(kinds)
	if err != nil {
		t.Fatal(err)
	}
	return service.Attach(directory)
}

func retentionFixtureKinds() []recordkind.Spec {
	return []recordkind.Spec{
		{ID: "entry", Match: func(name string) bool { return strings.HasPrefix(name, "entry-") }, Dependencies: func(record recordkind.Record) ([]string, error) {
			return []string{string(record.Data)}, nil
		}},
		{ID: "payload", Match: func(name string) bool { return strings.HasPrefix(name, "payload-") }},
	}
}

func ageRetentionCatalog(t *testing.T, store *Store, thread string) {
	t.Helper()
	catalog, err := store.ReadCatalog(SessionName(thread))
	if err != nil {
		t.Fatal(err)
	}
	catalog.LastUsed = time.Now().Add(-SessionRetention - time.Hour)
	data, err := json.Marshal(&catalog)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Transaction(t.Context(), nil, func(tx *Transaction) error {
		return tx.Write(SessionName(thread), "catalog-pending-", data)
	}); err != nil {
		t.Fatal(err)
	}
	// Permit another age sweep without a wall-clock wait.
	past := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(filepath.Join(store.directory, "retention-sweep"), past, past); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}

func requireRetentionFiles(t *testing.T, directory string, present bool, names ...string) {
	t.Helper()
	for _, name := range names {
		_, err := os.Stat(filepath.Join(directory, name))
		if present && err != nil || !present && !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s: want present=%v, stat error=%v", name, present, err)
		}
	}
}

func TestRetentionAbsentOwnerPreservesCatalogAndDependencies(t *testing.T) {
	directory := t.TempDir()
	kinds := retentionFixtureKinds()
	store := retentionFixtureStore(t, directory, kinds...)
	if err := store.Transaction(t.Context(), nil, func(tx *Transaction) error {
		if err := tx.Write("payload-shared", "payload-pending-", []byte("shared bytes")); err != nil {
			return err
		}
		if err := tx.Write("entry-old", "entry-pending-", []byte("payload-shared")); err != nil {
			return err
		}
		return tx.Retain("old", Limits{}, "entry-old")
	}); err != nil {
		t.Fatal(err)
	}
	ageRetentionCatalog(t, store, "old")
	catalog, err := store.ReadCatalog(SessionName("old"))
	if err != nil || !catalog.Files["payload-shared"] {
		t.Fatalf("dependency not adopted: catalog=%+v err=%v", catalog, err)
	}
	withoutOwner := retentionFixtureStore(t, directory, kinds[1])
	before, err := os.ReadFile(filepath.Join(directory, SessionName("old")))
	if err != nil {
		t.Fatal(err)
	}
	if err := withoutOwner.Cleanup(t.Context(), nil, Limits{}, "", nil); err != nil {
		t.Fatal(err)
	}
	requireRetentionFiles(t, directory, true, SessionName("old"), "entry-old", "payload-shared")
	after, err := os.ReadFile(filepath.Join(directory, SessionName("old")))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("absent owner changed retained catalog: before=%s after=%s err=%v", before, after, err)
	}
	reenabled := retentionFixtureStore(t, directory, kinds...)
	if err := reenabled.Cleanup(t.Context(), nil, Limits{}, "", nil); err != nil {
		t.Fatal(err)
	}
	requireRetentionFiles(t, directory, false, SessionName("old"), "entry-old", "payload-shared")
}

func TestRetentionSharedDependencySurvivesOneOwnerExpiration(t *testing.T) {
	directory := t.TempDir()
	store := retentionFixtureStore(t, directory, retentionFixtureKinds()...)
	if err := store.Transaction(t.Context(), nil, func(tx *Transaction) error {
		if err := tx.Write("payload-shared", "payload-pending-", []byte("shared bytes")); err != nil {
			return err
		}
		for _, thread := range []string{"old", "recent"} {
			name := "entry-" + thread
			if err := tx.Write(name, "entry-pending-", []byte("payload-shared")); err != nil {
				return err
			}
			if err := tx.Retain(thread, Limits{}, name); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ageRetentionCatalog(t, store, "old")
	if err := store.Cleanup(t.Context(), nil, Limits{}, "", nil); err != nil {
		t.Fatal(err)
	}
	requireRetentionFiles(t, directory, false, "entry-old", SessionName("old"))
	requireRetentionFiles(t, directory, true, "payload-shared", "entry-recent", SessionName("recent"))
	ageRetentionCatalog(t, store, "recent")
	if err := store.Cleanup(t.Context(), nil, Limits{}, "", nil); err != nil {
		t.Fatal(err)
	}
	requireRetentionFiles(t, directory, false, "payload-shared", "entry-recent", SessionName("recent"))
}

func TestRetentionAdmissionRequestsPressureWithoutSynchronousPruning(t *testing.T) {
	for _, budget := range []string{"aggregate", "separate"} {
		t.Run(budget, func(t *testing.T) {
			directory := t.TempDir()
			store := retentionFixtureStore(t, directory,
				recordkind.Spec{ID: "regular", Match: func(name string) bool { return strings.HasPrefix(name, "regular-") }},
				recordkind.Spec{ID: "separate", Match: func(name string) bool { return strings.HasPrefix(name, "separate-") }, SeparateBudget: true, Limit: 600},
			)
			if err := store.Transaction(t.Context(), nil, func(tx *Transaction) error {
				for name, size := range map[string]int{"regular-old": 100, "separate-old": 300} {
					if err := tx.Write(name, "record-pending-", []byte(strings.Repeat("x", size))); err != nil {
						return err
					}
				}
				return tx.Retain("owner", Limits{}, "regular-old", "separate-old")
			}); err != nil {
				t.Fatal(err)
			}
			limits := Limits{Kinds: map[string]int64{"separate": 400}}
			if err := store.Transaction(t.Context(), nil, func(tx *Transaction) error {
				files, err := tx.FileSizes()
				if err != nil {
					return err
				}
				limits.Total = files["regular-old"] + files[SessionName("owner")] + 100
				// Separate bytes cannot consume the aggregate allowance, while an
				// explicit kind limit overrides the contribution's default.
				if err := tx.Admit(limits, "regular-new", 100); err != nil {
					return err
				}
				if err := tx.Admit(limits, "separate-new", 100); err != nil {
					return err
				}
				name, size := "regular-new", int64(101)
				if budget == "separate" {
					name = "separate-new"
				}
				var pressure *PressureError
				if err := tx.Admit(limits, name, size); !errors.As(err, &pressure) || pressure.Name != name {
					t.Fatalf("admission error=%v, want pressure for %s", err, name)
				}
				pending, err := tx.PendingCleanup()
				if err == nil && !pending {
					t.Fatal("pressure did not request maintenance")
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			requireRetentionFiles(t, directory, true, "regular-old", "separate-old", SessionName("owner"))
			if err := store.Cleanup(t.Context(), nil, limits, "", nil); err != nil {
				t.Fatal(err)
			}
			requireRetentionFiles(t, directory, false, "regular-old", "separate-old", SessionName("owner"), "retention-pressure")
		})
	}
}
