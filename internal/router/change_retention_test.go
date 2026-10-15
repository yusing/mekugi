package router

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yusing/mekugi/contracts/recordkind"
)

func TestChangeRetirementPreparationPreservesSnapshotAndWireBytes(t *testing.T) {
	t.Parallel()
	name := changeIndexName("/w<&>", "")
	// Retained v1 spelling includes omitted zero fields and literal HTML.
	original := `{"Sequence":2,"Version":1,"Workspace":"/w<&>","Streams":[{"Name":"a","Thread":"old","Next":1}],"Changes":{"amber1":{"Correlation":"<&>","Calls":[{"Sequence":1,"ID":"first"},{"Sequence":2,"Thread":"other","ID":"last","Managed":true}]}}}`
	kind, found, err := testHistory().Kind(name)
	if err != nil || !found {
		t.Fatalf("change owner: %v", err)
	}
	prepare, err := kind.Retirement(t.Context(), recordkind.Record{Name: name, Data: []byte(original)})
	if err != nil {
		t.Fatal(err)
	}
	first := recordkind.Retirement{Deleted: map[string]bool{replayRecordName("/w<&>", "first", false): true}}
	want := `{"Sequence":2,"Version":1,"Workspace":"/w<&>","Streams":[{"Name":"a","Thread":"old","Next":1}],"Changes":{"amber1":{"Correlation":"<&>","Calls":[{"Sequence":2,"Thread":"other","ID":"last","Managed":true}],"RetiredCalls":1}}}`
	for range 2 {
		update, err := prepare(t.Context(), first)
		if err != nil || update == nil {
			t.Fatalf("prepare: update=%v err=%v", update, err)
		}
		if string(update.Data) != want {
			t.Fatalf("replacement bytes:\n%s\nwant:\n%s", update.Data, want)
		}
		last, err := update.Next(t.Context(), recordkind.Retirement{Deleted: map[string]bool{replayRecordName("/w<&>", "last", false): true}})
		if err != nil || last == nil {
			t.Fatalf("prepare next: update=%v err=%v", last, err)
		}
		if want := `{"Sequence":2,"Version":1,"Workspace":"/w<&>","Streams":[{"Name":"a","Thread":"old","Next":1,"Retired":1}],"Changes":{}}`; string(last.Data) != want {
			t.Fatalf("next replacement bytes:\n%s\nwant:\n%s", last.Data, want)
		}
	}

}

func TestChangeRetirementThroughSessionHistory(t *testing.T) {
	t.Parallel()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, release := retentionTestSession(t, store, "old", 0)
	id, err := store.reserveChange(ctx, "/w", "old", "correlation")
	if err != nil {
		t.Fatal(err)
	}
	histories := make(map[string]mekugiHistory)
	for i := range 35 {
		histories[fmt.Sprintf("call-%02d", i)] = mekugiHistory{ChangeID: id, CorrelationID: "correlation", Attempt: i + 1}
	}
	if err := store.put(ctx, "/w", histories); err != nil {
		t.Fatal(err)
	}
	if _, err := store.reserveChange(ctx, "/w", "old", "pending"); err != nil {
		t.Fatal(err)
	}
	release()
	retentionTestAge(t, store, "old", 30*24*time.Hour)
	name := changeIndexName("/w", store.scoped(ctx).handleNamespace())
	path := filepath.Join(store.directory, name)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	releaseSnapshot, err := store.lockStorageSnapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer releaseSnapshot()
	if err := store.cleanupSessions(t.Context()); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("leased index changed: %v", err)
	}
	for call := range histories {
		retentionTestExists(t, store, "/w", call, true)
	}
	releaseSnapshot()
	if err := store.cleanupSessions(t.Context()); err != nil {
		t.Fatal(err)
	}
	reopened, err := openMekugiReplayStore(store.directory)
	if err != nil {
		t.Fatal(err)
	}
	index, err := reopened.scoped(ctx).readChangeIndex("/w")
	if err != nil {
		t.Fatal(err)
	}
	if len(index.Changes) != 0 || index.Streams[0].Retired != 2 || index.Streams[0].Next != 2 {
		t.Fatalf("retirement lost allocation state: %+v", index)
	}
	for call := range histories {
		retentionTestExists(t, reopened, "/w", call, false)
	}
}
