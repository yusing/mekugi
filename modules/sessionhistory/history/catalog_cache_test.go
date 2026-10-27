package history_test

import (
	json "encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

func TestRetainCatalogRefreshAndFailure(t *testing.T) {
	directory := t.TempDir()
	store, peer := fixtureStore(directory, managedRecord), fixtureStore(directory, managedRecord)
	retain := func(names ...string) {
		t.Helper()
		if err := store.Transaction(t.Context(), nil, func(tx *history.Transaction) error {
			return tx.Retain("thread", history.Limits{}, names...)
		}); err != nil {
			t.Fatal(err)
		}
	}
	name := history.SessionName("thread")
	retain("original.json")
	for _, writer := range []*history.Store{peer, store} {
		catalog, err := writer.ReadCatalog(name)
		if err != nil {
			t.Fatal(err)
		}
		catalog.Files = map[string]bool{"replacement.json": true}
		data, err := json.Marshal(&catalog)
		if err != nil {
			t.Fatal(err)
		}
		if err := writer.Transaction(t.Context(), nil, func(tx *history.Transaction) error { return tx.Write(name, "catalog-", data) }); err != nil {
			t.Fatal(err)
		}
		// Refreshing size accounting must not bless the stale ownership cache.
		if err := store.Transaction(t.Context(), nil, func(tx *history.Transaction) error { _, err := tx.FileSizes(); return err }); err != nil {
			t.Fatal(err)
		}
		retain("original.json")
		got, err := store.ReadCatalog(name)
		if err != nil || !got.Files["original.json"] || !got.Files["replacement.json"] {
			t.Fatalf("catalog replacement was missed: %+v, %v", got, err)
		}
	}
	// Failed growth cannot mutate cached ownership and suppress a later retry.
	err := store.Transaction(t.Context(), nil, func(tx *history.Transaction) error {
		return tx.Retain("thread", history.Limits{Total: 1}, "retry.json")
	})
	if err == nil {
		t.Fatal("capacity failure missing")
	}
	retain("retry.json")
	got, err := store.ReadCatalog(name)
	if err != nil || !got.Files["retry.json"] {
		t.Fatalf("retry ownership lost: %+v, %v", got, err)
	}
	interrupted := errors.New("interrupted removal")
	err = store.Transaction(t.Context(), nil, func(tx *history.Transaction) error {
		if err := tx.AdvanceRevision(); err != nil {
			return err
		}
		if err := os.Remove(filepath.Join(directory, name)); err != nil {
			return err
		}
		return interrupted
	})
	if !errors.Is(err, interrupted) {
		t.Fatal(err)
	}
	retain("after.json")
	got, err = store.ReadCatalog(name)
	if err != nil || len(got.Files) != 1 || !got.Files["after.json"] {
		t.Fatalf("removed ownership resurrected: %+v, %v", got, err)
	}
}

func BenchmarkRetainWarmCatalog(b *testing.B) {
	for _, count := range []int{100, 10000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			store := fixtureStore(b.TempDir(), managedRecord)
			names := make([]string, count)
			for i := range names {
				names[i] = fmt.Sprintf("record-%d.json", i)
			}
			if err := store.Transaction(b.Context(), nil, func(tx *history.Transaction) error {
				if err := tx.Retain("thread", history.Limits{}, names...); err != nil {
					return err
				}
				return tx.Write("payload.json", "payload-", []byte("payload"))
			}); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			for b.Loop() {
				if err := store.Transaction(b.Context(), nil, func(tx *history.Transaction) error {
					return tx.Retain("thread", history.Limits{}, names[0])
				}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
