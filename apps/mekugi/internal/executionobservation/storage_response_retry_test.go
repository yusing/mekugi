package executionobservation

import (
	"context"

	"errors"

	"strings"

	"testing"

	"testing/synctest"

	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"
)

func TestResponsePersistencePressureStopsWithoutReclaimableData(t *testing.T) {
	t.Parallel()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	current, _ := retentionTestSession(t, store, "active", 0)
	retentionTestPut(t, store, current, "/w", "existing")
	files, err := store.testFileSizes()
	if err != nil {
		t.Fatal(err)
	}
	store.maxBytes, _ = store.publication.Usage(store.retentionLimits(), files, "", 0)
	startResponseRetentionWorker(t, store)
	ctx, cancel := context.WithTimeout(current, 3*time.Second)
	defer cancel()
	err = store.putAfterMaintenance(ctx, "/w", map[string]mekugiHistory{"new": {ToolName: "shell", Script: "true"}})
	if diagnostic, ok := errors.AsType[*requestexecution.DiagnosticError](err); !ok || diagnostic.Code != "storage_capacity" || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("protected storage must fail without endlessly retrying: %v", err)
	}
	retentionTestExists(t, store, "/w", "existing", true)
	retentionTestExists(t, store, "/w", "new", false)
}

func TestResponsePersistenceMaintenanceWaitIsCancellable(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		store, err := openMekugiReplayStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		current, _ := retentionTestSession(t, store, "active", 0)
		retentionTestPut(t, store, current, "/w", "existing")
		files, err := store.testFileSizes()
		if err != nil {
			t.Fatal(err)
		}
		store.maxBytes, _ = store.publication.Usage(store.retentionLimits(), files, "", 0)
		ctx, cancel := context.WithTimeout(current, 100*time.Millisecond)
		defer cancel()
		err = store.putAfterMaintenance(ctx, "/w", map[string]mekugiHistory{"new": {ToolName: "shell", Script: "true"}})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("maintenance wait ignored cancellation: %v", err)
		}
		if diagnostic, ok := errors.AsType[*requestexecution.DiagnosticError](err); !ok || diagnostic.Code != "storage_capacity" {
			t.Fatalf("wait lost original capacity diagnostic: %v", err)
		}
		retentionTestExists(t, store, "/w", "existing", true)
		retentionTestExists(t, store, "/w", "new", false)
	})
}

func TestResponsePersistenceRetriesSuccessiveHistoryWrites(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		store, err := openMekugiReplayStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		for _, thread := range []string{"old1", "old2", "old3"} {
			ctx, release := retentionTestSession(t, store, thread, 0)
			if err := store.put(ctx, "/w", map[string]mekugiHistory{
				thread: {ToolName: "shell", Script: strings.Repeat("old", 167)},
			}); err != nil {
				t.Fatal(err)
			}
			release()
			retentionTestAge(t, store, thread, time.Hour)
		}
		current, _ := retentionTestSession(t, store, "active", 0)
		files, err := store.testFileSizes()
		if err != nil {
			t.Fatal(err)
		}
		used, _ := store.publication.Usage(store.retentionLimits(), files, "", 0)
		store.maxBytes = used + 900
		startResponseRetentionWorker(t, store)
		ctx, cancel := context.WithTimeout(current, 5*time.Second)
		defer cancel()
		histories := map[string]mekugiHistory{
			"a": {ToolName: "shell", Script: strings.Repeat("a", 1000)},
			"b": {ToolName: "shell", Script: strings.Repeat("b", 1000)},
		}
		// Saving a succeeds after the first reclamation round, but fills enough
		// space to require another round for b. Partial publication is progress.
		if err := store.putAfterMaintenance(ctx, "/w", histories); err != nil {
			t.Fatal(err)
		}
		for id, want := range histories {
			got, found, err := store.lookup(ctx, "/w", id)
			if err != nil || !found || got.Script != want.Script {
				t.Fatalf("history %s not retained: found=%v err=%v history=%+v", id, found, err, got)
			}
		}
	})
}
