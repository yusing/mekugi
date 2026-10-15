package router

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/libraries/legacycall"
)

// Keep this wire fixture independent of the current encoder: these are the
// version-1 replay fields used before command segments had their own store.
// The numeric array is SHA-256("one\n"), not encoded per-segment output.
func commandSegmentsCompatEnvelope(workspace, id string) string {
	return fmt.Sprintf(`{
		"Version":1,"Workspace":%q,"CallID":%q,"Commentary":false,
		"History":{"CommandSegments":{
			"Command":"bash -lc 'echo one; false'","Exit":1,
			"Output":[44,139,8,218,92,230,3,152,225,241,154,240,229,220,204,116,77,242,116,184,38,171,229,133,234,186,104,197,37,67,72,6],
			"Parts":[
				{"Timing":{"started":"2025-01-02T03:04:05Z","ended":"2025-01-02T03:04:06Z","elapsed_ns":1000000000},"Source":"echo one","Skipped":false,"Exit":0,"Output":"one"},
				{"Source":"false","Skipped":false,"Exit":1,"Output":""}
			]
		}}
	}`, workspace, id)
}

func writeCommandSegmentsCompatEnvelope(t *testing.T, store *mekugiReplayStore, workspace, id, wire string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(store.directory, legacycall.Name(workspace, id, false)), []byte(wire), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestCommandSegmentsHistoricalCompatRoundTrip(t *testing.T) {
	workspace := t.TempDir()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	item, want := batchCommandSegmentsFixture("item")
	id := commandSegmentsID("turn", item.ID)
	want.Parts[0].Timing.Started = time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	want.Parts[0].Timing.Ended = want.Parts[0].Timing.Started.Add(time.Second)
	want.Parts[0].Timing.ElapsedNS = int64(time.Second)
	writeCommandSegmentsCompatEnvelope(t, store, workspace, id, commandSegmentsCompatEnvelope(workspace, id))
	got, err := readCommandSegments(store, workspace, "turn", item)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("historical sparse envelope: got %#v, error %v; want %#v", got, err, want)
	}
	// A current producer may retry an old identity despite omitted unrelated
	// legacy zero fields. Semantic equality, not identical JSON, decides this.
	if err := store.put(t.Context(), workspace, map[string]mekugiHistory{id: {CommandSegments: want}}); err != nil {
		t.Fatalf("same-identity retry: %v", err)
	}
	// Exercise the current writer too, rather than only its no-op retry path.
	copyID := commandSegmentsID("copy-turn", item.ID)
	if err := store.put(t.Context(), workspace, map[string]mekugiHistory{copyID: {CommandSegments: got}}); err != nil {
		t.Fatal(err)
	}
	roundTrip, err := readCommandSegments(store, workspace, "copy-turn", item)
	if err != nil || !reflect.DeepEqual(roundTrip, want) {
		t.Fatalf("round trip: got %#v, error %v; want %#v", roundTrip, err, want)
	}
	changedAggregate := item
	changedAggregate.AggregatedOutput = new("one")
	if got, err := readCommandSegments(store, workspace, "turn", changedAggregate); err != nil || got != nil {
		t.Fatalf("accepted changed aggregate bytes: %#v, %v", got, err)
	}
	changed := *want
	changed.Output[31] ^= 1
	if err := store.put(t.Context(), workspace, map[string]mekugiHistory{id: {CommandSegments: &changed}}); err == nil {
		t.Fatal("accepted different aggregate hash under an immutable identity")
	}
	if got, err := readCommandSegments(store, workspace, "turn", item); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("conflicting retry changed historical evidence: %#v, %v", got, err)
	}
}

func TestCommandSegmentsHistoricalCompatMixedHistory(t *testing.T) {
	workspace := t.TempDir()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	item, _ := batchCommandSegmentsFixture("mixed")
	id := commandSegmentsID("turn", item.ID)
	wire := strings.Replace(commandSegmentsCompatEnvelope(workspace, id), `"History":{`, `"History":{"Applied":true,"ToolName":"exec","Report":"retained report","UpstreamItem":{"provider_extension":{"opaque":[1,"two"]}},`, 1)
	writeCommandSegmentsCompatEnvelope(t, store, workspace, id, wire)
	if got, err := readCommandSegments(store, workspace, "turn", item); err != nil || got == nil {
		t.Fatalf("mixed historical segment read: %#v, %v", got, err)
	}
	before, found, err := store.read(workspace, id, false)
	if err != nil || !found {
		t.Fatalf("mixed historical record read: found %v, %v", found, err)
	}
	if before.History.ToolName != "exec" || before.History.Report != "retained report" || string(before.History.UpstreamItem["provider_extension"]) != `{"opaque":[1,"two"]}` {
		t.Fatalf("lost unrelated mixed history: %#v", before.History)
	}
	// Migrate a decoded mixed record through the writer without discarding its
	// non-segment facts or the opaque provider extension.
	copyID := commandSegmentsID("copy-turn", item.ID)
	if err := store.put(t.Context(), workspace, map[string]mekugiHistory{copyID: before.History}); err != nil {
		t.Fatal(err)
	}
	after, found, err := store.read(workspace, copyID, false)
	if err != nil || !found || !reflect.DeepEqual(after.History, before.History) {
		t.Fatalf("mixed history round trip: found %v, error %v; got %#v", found, err, after.History)
	}
}

func TestCommandSegmentsHistoricalCompatUnknownFields(t *testing.T) {
	// The legacy envelope is strict, including unused fields: ignoring an
	// unknown value must not hide either schema drift or malformed JSON.
	for _, tc := range []struct{ name, old, replacement string }{
		{"envelope", `"Version":1`, `"Future":{"valid":true},"Version":1`},
		{"history", `"History":{`, `"History":{"Future":[1,2],`},
		{"segment", `"Source":"false"`, `"Future":null,"Source":"false"`},
		{"malformed-unknown", `"Version":1`, `"Future":[1,],"Version":1`},
		{"malformed-legacy-field", `"History":{`, `"History":{"Applied":"true",`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace := t.TempDir()
			store, err := openMekugiReplayStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			item, _ := batchCommandSegmentsFixture("item")
			id := commandSegmentsID("turn", item.ID)
			wire := strings.Replace(commandSegmentsCompatEnvelope(workspace, id), tc.old, tc.replacement, 1)
			writeCommandSegmentsCompatEnvelope(t, store, workspace, id, wire)
			if got, err := readCommandSegments(store, workspace, "turn", item); err == nil || got != nil {
				t.Fatalf("accepted incompatible historical envelope: %#v, %v", got, err)
			}
		})
	}
}
