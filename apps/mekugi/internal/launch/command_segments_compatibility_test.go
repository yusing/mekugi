package launch

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yusing/mekugi/libraries/legacycall"
	"github.com/yusing/mekugi/modules/commandsegments/reports"
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

func TestCommandSegmentsHistoricalCompatMixedHistory(t *testing.T) {
	workspace := t.TempDir()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	item, _ := batchCommandSegmentsFixture("mixed")
	id := reports.ID("turn", item.ID)
	wire := strings.Replace(commandSegmentsCompatEnvelope(workspace, id), `"History":{`, `"TaskPath":"old/task","History":{"Applied":true,"JournalIDs":["old-item"],"ToolName":"exec","Report":"retained report","ReviewFiles":[{"AfterPath":"a","CreatedPath":"/old/a","Diff":"saved diff"}],"UpstreamItem":{"provider_extension":{"opaque":[1,"two"]}},`, 1)
	writeCommandSegmentsCompatEnvelope(t, store, workspace, id, wire)
	before, found, err := store.read(workspace, id, false)
	if err != nil || !found {
		t.Fatalf("mixed historical record read: found %v, %v", found, err)
	}
	if before.History.ToolName != "exec" || before.History.Report != "retained report" || string(before.History.UpstreamItem["provider_extension"]) != `{"opaque":[1,"two"]}` {
		t.Fatalf("lost unrelated mixed history: %#v", before.History)
	}
	if !reflect.DeepEqual(before.History.ReviewFiles, []legacycall.ReviewFile{{AfterPath: "a", Diff: "saved diff"}}) {
		t.Fatalf("retired creation metadata changed review: %#v", before.History.ReviewFiles)
	}
	// Migrate a decoded mixed record through the writer without discarding its
	// non-segment facts or the opaque provider extension.
	copyID := reports.ID("copy-turn", item.ID)
	if err := store.put(t.Context(), workspace, map[string]legacycall.History{copyID: before.History}); err != nil {
		t.Fatal(err)
	}
	after, found, err := store.read(workspace, copyID, false)
	if err != nil || !found || !reflect.DeepEqual(after.History, before.History) {
		t.Fatalf("mixed history round trip: found %v, error %v; got %#v", found, err, after.History)
	}
}
