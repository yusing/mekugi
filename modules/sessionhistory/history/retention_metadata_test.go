package history

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yusing/mekugi/contracts/recordkind"
)

func TestCleanupMetadataSkipsDecodersOnlyWithoutReclamation(t *testing.T) {
	for _, mode := range []string{"recent", "old orphan", "expired catalog", "pressure", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			indexDependencies, retirements := 0, 0
			store, decoded := pruneFixture(t, 1, 8, 1, recordkind.Spec{
				ID: "index", Match: func(n string) bool { return n == "index" }, Keep: true,
				Dependencies: func(recordkind.Record) ([]string, error) {
					indexDependencies++
					return []string{"payload-shared"}, nil
				},
				Retirement: func(context.Context, recordkind.Record) (recordkind.RetirementPlanner, error) {
					retirements++
					return nil, nil
				},
			})
			if err := store.Transaction(t.Context(), nil, func(tx *Transaction) error {
				if err := tx.Write("entry-orphan", "entry-pending-", []byte("x")); err != nil {
					return err
				}
				return tx.Write("index", "index-pending-", []byte("kept"))
			}); err != nil {
				t.Fatal(err)
			}
			past := time.Now().Add(-SessionRetention - time.Hour)
			if err := os.Chtimes(filepath.Join(store.directory, "index"), past, past); err != nil {
				t.Fatal(err)
			}
			limits := Limits{}
			switch mode {
			case "old orphan":
				if err := os.Chtimes(filepath.Join(store.directory, "entry-orphan"), past, past); err != nil {
					t.Fatal(err)
				}
			case "expired catalog":
				ageRetentionCatalog(t, store, "owner-0")
			case "pressure":
				limits.Total = 1
				if err := store.Transaction(t.Context(), nil, func(tx *Transaction) error {
					return tx.requestStoragePrune(storagePressureRequest{Name: "entry-new", Growth: 1})
				}); err != nil {
					t.Fatal(err)
				}
			case "unknown":
				if err := os.WriteFile(filepath.Join(store.directory, "unrecognized"), []byte("preserve"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			err := store.Cleanup(t.Context(), nil, limits, "", nil)
			if (err != nil) != (mode == "unknown") {
				t.Fatalf("cleanup: %v", err)
			}
			if mode == "recent" || mode == "unknown" {
				if *decoded != 0 || indexDependencies != 0 || retirements != 0 {
					t.Fatalf("metadata-only cleanup decoded entries=%d index=%d retirement=%d", *decoded, indexDependencies, retirements)
				}
			} else if *decoded == 0 || indexDependencies == 0 || retirements == 0 {
				t.Fatalf("reclamation skipped semantic inspection: entries=%d index=%d retirement=%d", *decoded, indexDependencies, retirements)
			}
			// The persistent owner pins this payload even after catalog retirement.
			requireRetentionFiles(t, store.directory, true, "index", "payload-shared")
			requireRetentionFiles(t, store.directory, mode != "old orphan", "entry-orphan")
			catalogSurvives := mode != "expired catalog" && mode != "pressure"
			requireRetentionFiles(t, store.directory, catalogSurvives, SessionName("owner-0"), "entry-0000")
		})
	}
}
