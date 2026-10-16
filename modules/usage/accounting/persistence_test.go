package accounting

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"github.com/yusing/mekugi/contracts/inference"

	"github.com/yusing/mekugi/modules/sessionhistory/history"

	"github.com/yusing/mekugi/libraries/persistence"
)

func TestPersistenceUsageCoalescesBurstAndRetainsDeltas(t *testing.T) {
	store := usageStoreFixture(t, t.TempDir())
	store.writes = new(persistence.Counter)
	synctest.Test(t, func(t *testing.T) {
		usage := storedUsageFixture(t, store)
		defer usage.Close()
		usage.MarkNew("burst")
		for range 10 {
			usage.Observation("burst", "burst", "gpt-6-sol", "").Observe(inference.Usage{InputTokens: 10, UncachedInputTokens: 10, OutputTokens: 2})
		}
		synctest.Wait()
		time.Sleep(24 * time.Millisecond)
		synctest.Wait()
		if _, err := os.Stat(filepath.Join(store.directory, threadUsageName("burst"))); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("usage published before coalescing window: %v", err)
		}
		if store.writes.Snapshot().Bytes != 0 {
			t.Fatal("coalescing window wrote managed bytes")
		}
		flushUsageForTest(t, usage)
		retained, err := readThreadUsage(store.publication, "burst")
		if err != nil || retained == nil {
			t.Fatalf("retained usage: %v, %v", retained, err)
		}
		if retained.roundtrips != 10 || retained.counts.InputTokens != 100 || retained.counts.OutputTokens != 20 {
			t.Fatalf("burst lost or duplicated pending deltas: %+v", retained)
		}
		record, err := os.ReadFile(filepath.Join(store.directory, threadUsageName("burst")))
		if err != nil {
			t.Fatal(err)
		}
		revision, err := os.ReadFile(filepath.Join(store.directory, "storage-revision"))
		if err != nil {
			t.Fatal(err)
		}
		catalog, err := os.ReadFile(filepath.Join(store.directory, history.SessionName("burst")))
		if err != nil {
			t.Fatal(err)
		}
		// The first usage publication also retains its session catalog. Each
		// changed record advances the revision once; the burst needs only one
		// usage record and one catalog publication.
		if got, want := store.writes.Snapshot().Bytes, uint64(len(record)+len(catalog)+2*len(revision)); got != want {
			t.Fatalf("burst wrote %d bytes, want one publication of %d", got, want)
		}
		for range 3 {
			usage.Observation("burst", "burst", "gpt-6-sol", "").Observe(inference.Usage{InputTokens: 10, UncachedInputTokens: 10, OutputTokens: 2})
		}
		flushUsageForTest(t, usage)
		retained, err = readThreadUsage(store.publication, "burst")
		if err != nil || retained == nil || retained.roundtrips != 13 || retained.counts.InputTokens != 130 || retained.counts.OutputTokens != 26 {
			t.Fatalf("followup burst lost or duplicated deltas: %+v, %v", retained, err)
		}
	})
}
